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

// SystemClientViolation 是一条"SystemClient 出现在用户请求路径上"的违规。
type SystemClientViolation struct {
	Component string
	File      string
	Line      int
}

// systemClientDangerDirs 是 Go 组件里 SystemClient 不许出现的两个目录——
// 它们就是"用户请求路径"本身。backend/module/（Start()）与事件 handler
// （不落在这两个目录下的任何地方）都不受这条扫描管，那是它的合法出现
// 位置（设计书 §14.2.6、导读第 21 条）。
var systemClientDangerDirs = []string{"backend/internal/http", "backend/internal/grpc"}

// pySystemClientDangerDirs / tsSystemClientDangerDirs 是 Python/TS 组件的
// 对应目录（阶段三 Task 3 新拍板、总纲 SOP-B 补记的约定）：
//   - Python 组件的 backend/app/http、backend/app/grpc 对应 Go 的
//     backend/internal/http、backend/internal/grpc——REST handler 与 gRPC
//     handler 都可能在服务内部再调别的依赖，同样要走 UserClient 转发
//     调用者身份，不能用 SystemClient。
//   - TS（infra-bff-mobile）没有"路由注册"这个动作，请求路径就是 GraphQL
//     resolver 本身；本项目约定 resolver map 一律放在 src/resolvers/ 下
//     （模仿 Go 的"只有一个合法目录"这条设计，把误伤面钉死到最小）。
var pySystemClientDangerDirs = []string{"backend/app/http", "backend/app/grpc"}
var tsSystemClientDangerDirs = []string{"src/resolvers"}

// pySystemClientCallRe / tsSystemClientCallRe 认的是函数名，不是"besdk 的
// 东西都可疑"——同 Go 版判据（导读第 21 条），要放行的是 user_client/
// userClient。⚠️ 已知精度上限：正则不理解字符串字面量/多行结构，比
// go/ast 粗——但这两个危险目录本来就是"除了 handler 逻辑不该有别的
// 东西"的约定死目录，命中即代表这个目录里出现了这个词，宁可让人再看
// 一眼，也不能像"零机器守卫"那样悄悄放过（这条路径不报错，错了也没有
// 任何症状）。
var pySystemClientCallRe = regexp.MustCompile(`\bsystem_client\s*\(`)
var tsSystemClientCallRe = regexp.MustCompile(`\bsystemClient\s*\(`)

// SystemClientScan 扫描 root/components/ 下每个组件的请求处理目录，
// 找 SystemClient(...) 调用——Go/Python/TS 三种约定目录都扫，一个组件
// 只会真的落在其中一种（一个组件一种语言，不混栈），互不干扰。
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

		vs, err := scanGoSystemClient(compDir, id, fset)
		if err != nil {
			return nil, err
		}
		violations = append(violations, vs...)

		for _, sub := range pySystemClientDangerDirs {
			vs, err := scanTextForPattern(filepath.Join(compDir, sub), id, ".py", pySystemClientCallRe)
			if err != nil {
				return nil, err
			}
			violations = append(violations, toSystemClientViolations(vs)...)
		}
		for _, sub := range tsSystemClientDangerDirs {
			vs, err := scanTextForPattern(filepath.Join(compDir, sub), id, ".ts", tsSystemClientCallRe)
			if err != nil {
				return nil, err
			}
			violations = append(violations, toSystemClientViolations(vs)...)
		}
	}
	return violations, nil
}

func scanGoSystemClient(compDir, id string, fset *token.FileSet) ([]SystemClientViolation, error) {
	var violations []SystemClientViolation
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
	return violations, nil
}

func toSystemClientViolations(hits []textHit) []SystemClientViolation {
	out := make([]SystemClientViolation, 0, len(hits))
	for _, h := range hits {
		out = append(out, SystemClientViolation{Component: h.Component, File: h.File, Line: h.Line})
	}
	return out
}
