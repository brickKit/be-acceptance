// Package gates 装铁律六（组件互不 import）等跨仓库才看得出来的门禁。
// 逐仓库能查的（如铁律七 module-check）不在这里——那些落在各组件自己的
// Makefile（总纲 §I 门禁 9）。
package gates

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Violation 是一条铁律六违规：From 组件 import 了 To 组件。
type Violation struct {
	From string // 组件 ID，如 "erp/sales"
	To   string // 组件 ID，如 "mdm/customer"
	File string
	Line int
}

// brickKitOrgPrefix 是本组织下所有仓库共享的 module 前缀。铁律六的判据
// 不依赖"这个仓库当前是否在 components/ 下能查到"——见 allowedShared 的
// 注释：命中这个前缀又不在白名单里，就是违规，不管对方仓库是否真实存在
// 于本次装配（决策 91 要禁的正是"抽一个公共 model 包"，那个包往往根本
// 不是任何组件，是另一个独立仓库）。
const brickKitOrgPrefix = "github.com/brickKit/"

// allowedShared 是第一类白名单：横切基础库。
// ⚠️ 往这里加东西之前先回答一个问题：它有没有业务逻辑或组件 model？
// 有就不许加——那正是铁律六要禁的（§13.3）。
var allowedShared = map[string]bool{
	"github.com/brickKit/be-sdk-go":     true,
	"github.com/brickKit/be-sdk-python": true,
	"github.com/brickKit/be-sdk-ts":     true,
}

// isGeneratedContractImport 判断一条跨组件 import 是不是第二类白名单：某组件
// 自己发布的生成物契约包（`gen/<domain>/<name>`，protoc-gen-go/-grpc 直接
// 生成，只含消息类型与客户端 stub，不含任何业务逻辑）。
//
// 加这一类白名单的原因：阶段四合并部署时发现，erp-sales 曾经"逐字复制"一份
// 别的组件的生成代码放在自己仓库里当"只读镜像"（vendored-contract 模式），
// 图的是不直接 import 别的组件仓库；但一旦两边被分进同一个外壳、编译进同一
// 个进程，两份内容相同、import path 不同的生成代码会各自在 protobuf 全局
// 注册表里注册一次同一个文件/类型全名，直接 panic——Go 的 module system
// 无法把两个不同 import path 的包合并成一份编译实例，`replace` 也解决不了
// （阶段四调研记录 04 §13 有完整推演，含最小复现）。真正的解法是让"生成物
// 契约包"本身升级为第二类白名单——与 be-sdk-* 同类：横切、无业务逻辑，
// 各组件直接 import 彼此的真身，不再各自逐字复制一份。
//
// 判据：路径形如 `github.com/brickKit/<repo>/gen/<...>`——即组织前缀之后
// 第二段字面量是 "gen"。按约定这一层必须独立成嵌套 go module（module 边界
// 切在协议版本目录 v1 的上一级，因为 Go 模块路径禁止以字面量 /v1 结尾），
// 这样发布/引用走的是正常的 go get/require，不是文件复制。
func isGeneratedContractImport(path string) bool {
	rest := strings.TrimPrefix(path, brickKitOrgPrefix)
	parts := strings.SplitN(rest, "/", 3)
	return len(parts) >= 2 && parts[1] == "gen"
}

// ImportScan 扫描 root/components/ 下每个组件目录，返回全部违规。
//
// ⚠️ 目前只扫 Go（go/ast 解析 import 块）。Python 组件出现前不实现 Python
// 扫描——没有真实 Python 组件可以核对"import 路径该长什么样"，写一份没
// 验证过的解析规则，比没有更危险：它会让 Python 组件的铁律六违规悄悄放行，
// 而 make gates 一路绿灯（正是决策 91 要防的那种没有症状的坑）。加 Python
// 支持前先确认阶段四第一个真实 Python 组件的 import 写法。
func ImportScan(root string) ([]Violation, error) {
	componentsDir := filepath.Join(root, "components")
	dirs := componentDirs(componentsDir)

	repoToComponent := make(map[string]string) // module path -> 组件 ID
	ownModule := make(map[string]string)       // 组件目录 -> 自己的 module path
	for _, dir := range dirs {
		mod, err := moduleOf(dir)
		if err != nil {
			return nil, err
		}
		if mod == "" {
			continue // 非 Go 组件（如 Python），本函数管不到
		}
		id := componentID(componentsDir, dir)
		repoToComponent[mod] = id
		ownModule[dir] = mod
	}

	var violations []Violation
	for _, compDir := range dirs {
		mod, ok := ownModule[compDir]
		if !ok {
			continue
		}
		fromID := componentID(componentsDir, compDir)
		vs, err := scanDir(compDir, fromID, mod, repoToComponent)
		if err != nil {
			return nil, err
		}
		violations = append(violations, vs...)
	}
	return violations, nil
}

// moduleOf 读一个组件目录的 go.mod，返回它的 module path；没有 go.mod
// 返回空字符串（不是错误——组件可能是 Python）。
func moduleOf(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module")), nil
		}
	}
	return "", nil
}

// componentDirs 枚举 root/components/<scope>/<name>/ 形态的组件目录，
// 跳过 .archived（归档的不参与本次装配）。
func componentDirs(componentsDir string) []string {
	var dirs []string
	scopes, err := os.ReadDir(componentsDir)
	if err != nil {
		return nil
	}
	for _, scope := range scopes {
		if !scope.IsDir() || strings.HasPrefix(scope.Name(), ".") {
			continue
		}
		scopeDir := filepath.Join(componentsDir, scope.Name())
		names, err := os.ReadDir(scopeDir)
		if err != nil {
			continue
		}
		for _, name := range names {
			if !name.IsDir() {
				continue
			}
			dirs = append(dirs, filepath.Join(scopeDir, name.Name()))
		}
	}
	return dirs
}

// componentID 把 root/components/erp/sales 变成 "erp/sales"。
func componentID(componentsDir, compDir string) string {
	rel, err := filepath.Rel(componentsDir, compDir)
	if err != nil {
		return compDir
	}
	return filepath.ToSlash(rel)
}

// scanDir 用 go/parser 解析组件目录**及其全部子目录**下每个 .go 文件的
// import 块，命中本组织下、白名单之外、且不是自己 module 的路径就记一条
// 违规。
//
// ⚠️⚠️ 阶段四 Task 10 真机验证拆回门禁时才发现的严重 bug：这个函数原来
// 只用 os.ReadDir 读**一层**目录、遇到子目录直接跳过（`if e.IsDir() {
// continue }`，没有任何递归调用）——而每个组件真正的业务代码全部在
// `backend/...`/`gen/...` 这类嵌套目录里，`components/<scope>/<name>/`
// 这一层本身通常一个 .go 文件都没有。也就是说本函数从建仓库第一天起就
// 一直在扫一个空目录：故意在 `erp-sales` 的
// `backend/internal/client/client.go` 里加一行真实的跨组件 import
// （`crm-opportunity/backend/internal/service`）验证时，`make gates`
// 依然打印"0 条违规"——铁律六的守卫其实从来没有真的守过任何东西，
// `importscan_test.go` 原有的全部用例也凑巧只在组件目录**顶层**放测试
// 文件（`components/erp/sales/svc.go`），同一个盲区连测试自己都没有
// 覆盖到，才让这个 bug 藏到现在。现在改成真正递归整棵目录树（用
// filepath.WalkDir，跳过以 "." 开头的目录——不需要专门处理
// `components/.archived`，那一层在 componentDirs 阶段就已经被排除，
// 这里的隐藏目录判据只是防御性地不进任何组件自己内部可能出现的隐藏
// 目录，比如 `.git`）。
func scanDir(dir, fromID, ownModule string, repoToComponent map[string]string) ([]Violation, error) {
	var violations []Violation
	fset := token.NewFileSet()

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}

		f, ferr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if ferr != nil {
			return fmt.Errorf("解析 %s：%w", path, ferr)
		}
		for _, imp := range f.Imports {
			impPath, uerr := strconv.Unquote(imp.Path.Value)
			if uerr != nil {
				continue
			}
			if !strings.HasPrefix(impPath, brickKitOrgPrefix) {
				continue // 标准库、第三方，不归铁律六管
			}
			if allowedShared[impPath] {
				continue
			}
			if isGeneratedContractImport(impPath) {
				continue
			}
			if impPath == ownModule || strings.HasPrefix(impPath, ownModule+"/") {
				continue // 自己内部的包
			}
			pos := fset.Position(imp.Pos())
			violations = append(violations, Violation{
				From: fromID,
				To:   resolveComponentID(impPath, repoToComponent),
				File: path,
				Line: pos.Line,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return violations, nil
}

// resolveComponentID 优先用已知组件仓库映射（路径可能带子包，如
// ".../mdm-customer/model"）；查不到就退化为 "组织前缀之后的第一段"——
// 对方可能是本次装配根本看不到的独立仓库（如被抽出去的"公共 model 包"）。
func resolveComponentID(importPath string, repoToComponent map[string]string) string {
	for repo, id := range repoToComponent {
		if importPath == repo || strings.HasPrefix(importPath, repo+"/") {
			return id
		}
	}
	rest := strings.TrimPrefix(importPath, brickKitOrgPrefix)
	return strings.SplitN(rest, "/", 2)[0]
}
