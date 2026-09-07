package gates

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// BareGinViolation 是一条"业务代码裸用 gin 路由方法"的违规。
type BareGinViolation struct {
	Component string
	File      string
	Line      int
	Method    string
}

var ginRouteMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true,
}

// BareGinScan 扫描 root/components/ 下每个组件的 backend/internal/http/
// 目录，找形如 g.GET(...)/engine.POST(...) 这类直接在 gin 对象上调用的
// 路由注册——绕开了 besdk.GET/POST/PUT/PATCH/DELETE(r, path, perm, h)
// 的权限键签名强制，那个接口从此无人鉴权且完全没有任何症状
// （设计书 §14.1.7、导读第 23 条）。
//
// ⚠️ 判据不能是纯文本匹配 `\.(GET|POST|...)\(`：besdk.GET(g, ...) 字面上
// 也含有这个子串。用 go/ast 看清调用的接收者是不是标识符 "besdk"，
// 是就放行，不是（包括另一个对象、或链式调用）就判违规——这也顺带
// 免了 grep 方案必须先去掉行内注释才能用的麻烦（阶段一 D5 踩过）。
func BareGinScan(root string) ([]BareGinViolation, error) {
	componentsDir := filepath.Join(root, "components")
	dirs := componentDirs(componentsDir)

	var violations []BareGinViolation
	fset := token.NewFileSet()
	for _, compDir := range dirs {
		id := componentID(componentsDir, compDir)
		scanDir := filepath.Join(compDir, "backend/internal/http")
		entries, err := os.ReadDir(scanDir)
		if err != nil {
			continue // 组件可能没有 REST 面
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			filePath := filepath.Join(scanDir, e.Name())
			f, err := parser.ParseFile(fset, filePath, nil, 0)
			if err != nil {
				return nil, fmt.Errorf("解析 %s：%w", filePath, err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !ginRouteMethods[sel.Sel.Name] {
					return true
				}
				if recv, ok := sel.X.(*ast.Ident); ok && recv.Name == "besdk" {
					return true // besdk.GET(...) 是合法调用
				}
				pos := fset.Position(call.Pos())
				violations = append(violations, BareGinViolation{
					Component: id, File: filePath, Line: pos.Line, Method: sel.Sel.Name,
				})
				return true
			})
		}
	}
	return violations, nil
}
