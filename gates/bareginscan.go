package gates

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// BareRouteViolation 是一条"业务代码绕开权限键强制、裸注册请求入口"的
// 违规。Method 在三种语言里含义略有不同（Go：gin 的路由方法名；Python：
// FastAPI 的路由方法名；TS：固定写 "resolver"），但都是"这里绕开了
// besdk 的权限键签名强制"这同一件事（导读第 23 条）。
type BareRouteViolation struct {
	Component string
	File      string
	Line      int
	Method    string
}

var ginRouteMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true,
}

// pyBareDecoratorRe 认 FastAPI 原生的路由装饰器 @router.get(...)/
// @app.post(...)。判据只认装饰器形态，不认"任意对象.get(...)"这种纯
// 方法调用——Python 里 .get( 是极常见的合法用法（dict.get/环境变量取值/
// 配置读取…），当成裸路由会把这些全部误判；而 besdk.get(router, path,
// perm, handler) 从来不是装饰器用法（它的签名要求 router 是第一个位置
// 参数），所以"@ 开头 + .方法名(" 这个组合在真实 FastAPI 代码里只可能是
// 原生路由注册，精度足够。
var pyBareDecoratorRe = regexp.MustCompile(`^\s*@\w+\.(get|post|put|patch|delete)\s*\(`)

// pyAddAPIRouteRe 认另一种绕开 besdk 的写法：直接调 add_api_route。
var pyAddAPIRouteRe = regexp.MustCompile(`\.add_api_route\s*\(`)

// tsResolverFieldRe 认"字段名: 值"这个 resolver map 条目的形状；
// tsBareFunctionValueRe 认值是不是"看起来像一个函数"（箭头函数/
// function 关键字），据此判断这个值有没有经过 requirePermission(...)
// 包一层。⚠️ 已知精度上限：只按单行匹配，箭头函数的参数列表换行到下一
// 行时这条扫描看不见——但这条判据只扫 src/resolvers/ 这一个约定死的
// 目录（这里不该有别的东西），命中的假阴性风险由"这个目录只放
// resolver"这条硬约定兜底，而不是靠正则本身做到滴水不漏。
var tsResolverFieldRe = regexp.MustCompile(`^\s*(\w+)\s*:\s*(.*)$`)
var tsBareFunctionValueRe = regexp.MustCompile(`^(async\s+)?(\([^)]*\)\s*(:\s*[^=]+)?\s*=>|function\b)`)

// BareRouteScan 扫描 root/components/ 下每个组件的请求注册面，找绕开
// besdk 权限键包装、直接调用底层框架路由/装饰器/resolver 的写法——
// Go 扫 backend/internal/http（gin）、Python 扫 backend/app/http
// （FastAPI）、TS 扫 src/resolvers（GraphQL resolver map）。三者判据
// 形状不同（导读第 23 条、设计书 §14.1.7；GraphQL 场景的判据是阶段三
// Task 3 新设计的，"resolver 是否经过权限键包装"，不是照抄 Go 版能过的）。
func BareRouteScan(root string) ([]BareRouteViolation, error) {
	componentsDir := filepath.Join(root, "components")
	dirs := componentDirs(componentsDir)

	var violations []BareRouteViolation
	fset := token.NewFileSet()
	for _, compDir := range dirs {
		id := componentID(componentsDir, compDir)

		vs, err := scanGoBareRoute(compDir, id, fset)
		if err != nil {
			return nil, err
		}
		violations = append(violations, vs...)

		vs, err = scanPyBareRoute(compDir, id)
		if err != nil {
			return nil, err
		}
		violations = append(violations, vs...)

		vs, err = scanTSBareResolver(compDir, id)
		if err != nil {
			return nil, err
		}
		violations = append(violations, vs...)
	}
	return violations, nil
}

// scanGoBareRoute 用 go/ast 判断调用的接收者是不是标识符 "besdk"，是就
// 放行，不是（包括另一个对象、或链式调用）就判违规——这也顺带免了 grep
// 方案必须先去掉行内注释才能用的麻烦（阶段一 D5 踩过）。
func scanGoBareRoute(compDir, id string, fset *token.FileSet) ([]BareRouteViolation, error) {
	scanDir := filepath.Join(compDir, "backend/internal/http")
	entries, err := os.ReadDir(scanDir)
	if err != nil {
		return nil, nil // 组件可能没有 REST 面
	}
	var violations []BareRouteViolation
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
			violations = append(violations, BareRouteViolation{
				Component: id, File: filePath, Line: pos.Line, Method: sel.Sel.Name,
			})
			return true
		})
	}
	return violations, nil
}

// scanPyBareRoute 扫 backend/app/http 下的 .py 文件，找原生 FastAPI 路由
// 装饰器与 add_api_route 直调——besdk.get/post/put/patch/delete(router,
// path, perm, handler) 是普通函数调用，从不以装饰器形态出现。
func scanPyBareRoute(compDir, id string) ([]BareRouteViolation, error) {
	scanDir := filepath.Join(compDir, "backend/app/http")
	entries, err := os.ReadDir(scanDir)
	if err != nil {
		return nil, nil
	}
	var violations []BareRouteViolation
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".py") || isTestFile(e.Name(), ".py") {
			continue
		}
		filePath := filepath.Join(scanDir, e.Name())
		lines, err := readLines(filePath)
		if err != nil {
			return nil, err
		}
		for i, line := range lines {
			if m := pyBareDecoratorRe.FindStringSubmatch(line); m != nil {
				violations = append(violations, BareRouteViolation{
					Component: id, File: filePath, Line: i + 1, Method: m[1],
				})
				continue
			}
			if pyAddAPIRouteRe.MatchString(line) {
				violations = append(violations, BareRouteViolation{
					Component: id, File: filePath, Line: i + 1, Method: "add_api_route",
				})
			}
		}
	}
	return violations, nil
}

// scanTSBareResolver 扫 src/resolvers 下的 .ts 文件，找值直接是函数（箭头
// 函数/function 关键字）、没有先经过 requirePermission(...) 包一层的
// resolver map 字段。
func scanTSBareResolver(compDir, id string) ([]BareRouteViolation, error) {
	scanDir := filepath.Join(compDir, "src/resolvers")
	entries, err := os.ReadDir(scanDir)
	if err != nil {
		return nil, nil
	}
	var violations []BareRouteViolation
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ts") || isTestFile(e.Name(), ".ts") {
			continue
		}
		filePath := filepath.Join(scanDir, e.Name())
		lines, err := readLines(filePath)
		if err != nil {
			return nil, err
		}
		for i, line := range lines {
			m := tsResolverFieldRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			field, value := m[1], strings.TrimSpace(m[2])
			if strings.HasPrefix(value, "requirePermission(") {
				continue // 已经包了一层，合法
			}
			if tsBareFunctionValueRe.MatchString(value) {
				violations = append(violations, BareRouteViolation{
					Component: id, File: filePath, Line: i + 1, Method: field,
				})
			}
		}
	}
	return violations, nil
}

func readLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return strings.Split(string(data), "\n"), nil
}
