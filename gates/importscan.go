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

// allowedShared 是唯一的白名单：横切基础库。
// ⚠️ 往这里加东西之前先回答一个问题：它有没有业务逻辑或组件 model？
// 有就不许加——那正是铁律六要禁的（§13.3）。
var allowedShared = map[string]bool{
	"github.com/brickKit/be-sdk-go":     true,
	"github.com/brickKit/be-sdk-python": true,
	"github.com/brickKit/be-sdk-ts":     true,
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

// scanDir 用 go/parser 解析目录下每个 .go 文件的 import 块，命中本组织
// 下、白名单之外、且不是自己 module 的路径就记一条违规。
func scanDir(dir, fromID, ownModule string, repoToComponent map[string]string) ([]Violation, error) {
	var violations []Violation
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		filePath := filepath.Join(dir, e.Name())
		f, err := parser.ParseFile(fset, filePath, nil, parser.ImportsOnly)
		if err != nil {
			return nil, fmt.Errorf("解析 %s：%w", filePath, err)
		}
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			if !strings.HasPrefix(path, brickKitOrgPrefix) {
				continue // 标准库、第三方，不归铁律六管
			}
			if allowedShared[path] {
				continue
			}
			if path == ownModule || strings.HasPrefix(path, ownModule+"/") {
				continue // 自己内部的包
			}
			pos := fset.Position(imp.Pos())
			violations = append(violations, Violation{
				From: fromID,
				To:   resolveComponentID(path, repoToComponent),
				File: filePath,
				Line: pos.Line,
			})
		}
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
