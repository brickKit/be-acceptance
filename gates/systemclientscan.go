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

// SystemClientViolation 是一条"SystemClient 出现在用户请求路径上"的违规。
type SystemClientViolation struct {
	Component string
	File      string
	Line      int
}

// systemClientDangerDirs 是 SystemClient 不许出现的两个目录——它们就是
// "用户请求路径"本身。Start()（backend/module/）与事件 handler
// （不落在这两个目录下的任何地方）都不受这条扫描管，那是它的合法出现
// 位置（设计书 §14.2.6、导读第 21 条）。
var systemClientDangerDirs = []string{"backend/internal/http", "backend/internal/grpc"}

// SystemClientScan 扫描 root/components/ 下每个组件的请求处理目录，
// 找 besdk.SystemClient(...) 调用。
//
// ⚠️ 用 SystemClient 绕过数据权限不报错，返回的数据只是「多了一些」——
// 这是全项目第三条「悄悄读到别人数据」的路径，没有机器守，人永远发现
// 不了。
func SystemClientScan(root string) ([]SystemClientViolation, error) {
	componentsDir := filepath.Join(root, "components")
	dirs := componentDirs(componentsDir)

	var violations []SystemClientViolation
	fset := token.NewFileSet()
	for _, compDir := range dirs {
		id := componentID(componentsDir, compDir)
		for _, sub := range systemClientDangerDirs {
			scanDir := filepath.Join(compDir, sub)
			entries, err := os.ReadDir(scanDir)
			if err != nil {
				continue // 目录不存在很正常：组件可能没有 REST 面，或还没写 gRPC handler
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
					if !ok || sel.Sel.Name != "SystemClient" {
						return true
					}
					pos := fset.Position(call.Pos())
					violations = append(violations, SystemClientViolation{
						Component: id, File: filePath, Line: pos.Line,
					})
					return true
				})
			}
		}
	}
	return violations, nil
}
