// Package versionbump 自动传播一次组件版本变更：算出哪些别的组件/外壳
// 因为依赖它而也要跟着升版本号（依赖版本号同步，踩坑记录 C16 复发过 4
// 次的那类坑），把每个组件自己的 component.yaml（含 dependencies.components
// 引用）、外壳 component.yaml 的 shell.members、外壳 go.mod 的 require
// 全部改到位。
//
// brickKit v1 之后不再做的三件事（见 dev/test-records/06a/
// task10-version-checks-experiment.md）：改 brickkit.yaml 顶层 pin
// （`brickkit upgrade` 负责）、同步 config: 里的主机名字面量（`$var:`
// 取代）、改 AGENTS 组件名册（CLI 维护块取代）。
//
// ⚠️ 故意不引入 YAML 库，用正则在文本层面精确定位要改的那一行——原因
// 与 gates.DependencyVersionScan 相同：component.yaml 里逐行的行内注释
// （尤其是"这个组件特有的坑"里那些 ⚠️ 批注）是内容本身，不是装饰，
// 一次"解析再序列化"的 YAML 库往返会把它们全部冲掉。这个包只做"找到
// 这一行、原地替换这一行里的版本号子串、在旁边插一行新的变更记录"，
// 文件的其余每一个字节都不动。
package versionbump

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Component 是从某个 component.yaml 里读出来的、这个包关心的最小切面。
type Component struct {
	ID      string   // "erp/inventory"
	Dir     string   // 绝对路径，components/erp/inventory 或外壳 shell/be/go-core
	Version string   // metadata.version 当前值，如 "1.0.11"
	DepIDs  []string // dependencies.components + shell.members 里引用到的组件 ID（不含版本号），用来建反向依赖图
}

// RepoName 把 "erp/inventory" 变成 "erp-inventory"——AGENTS.md 组件名录表
// 用的是仓库名，brickkit up 生成的容器名/主机名前缀也是这个形状。
func (c *Component) RepoName() string {
	return strings.ReplaceAll(c.ID, "/", "-")
}

var metaVersionRe = regexp.MustCompile(`(?m)^\s*version:\s*([0-9]+\.[0-9]+\.[0-9]+)`)
var depRefRe = regexp.MustCompile(`\b([a-z][a-z0-9_-]*/[a-z][a-z0-9_-]*)@([0-9]+\.[0-9]+\.[0-9]+)\b`)

// componentDirs/componentID/topLevelBlock 是 gates 包里同名私有函数的
// 对应实现——两个包各自独立维护同一份技巧，同 gates 目录下每个
// gate 文件"自包含、不互相借用私有实现"的既有约定（见
// dependencyversionscan.go 顶部注释），不为了省几十行代码而在两个包
// 之间建一条耦合边。

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

func componentID(componentsDir, compDir string) string {
	rel, err := filepath.Rel(componentsDir, compDir)
	if err != nil {
		return compDir
	}
	return filepath.ToSlash(rel)
}

// topLevelBlock 从"顶格 <key>"这一行开始，收集到下一个顶格行为止的
// 整块文本（含首行）。
func topLevelBlock(path, key string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	lines := strings.Split(string(data), "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, key) {
			start = i
			break
		}
	}
	if start == -1 {
		return "", nil
	}
	var block strings.Builder
	block.WriteString(lines[start])
	block.WriteByte('\n')
	for i := start + 1; i < len(lines); i++ {
		l := lines[i]
		if strings.TrimSpace(l) == "" {
			continue
		}
		if !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "\t") {
			break
		}
		block.WriteString(l)
		block.WriteByte('\n')
	}
	return block.String(), nil
}

// shellDirs 列出 root/shell/<scope>/<name>（外壳是独立仓库，
// 以子模块挂在装配仓库的 shell/be/<name>/），目录形状同 components/<scope>/<name>。
func shellDirs(root string) []string {
	return componentDirs(filepath.Join(root, "shell"))
}

// LoadRegistry 扫 root/components/<scope>/<name>/component.yaml 与
// root/shell/<scope>/<name>/component.yaml，建出全部组件和外壳的
// id → Component 索引，供后面算级联用。跳过 .archived、跳过没有
// component.yaml 的目录。外壳通过 shell.members 依赖它编译进去的成员，
// 所以成员升版本会把外壳也级联进来。
func LoadRegistry(root string) (map[string]*Component, error) {
	reg := map[string]*Component{}
	scan := func(baseDir string, dirs []string) error {
		for _, dir := range dirs {
			yamlPath := filepath.Join(dir, "component.yaml")
			if _, err := os.Stat(yamlPath); err != nil {
				continue
			}
			id := componentID(baseDir, dir)

			metaBlock, err := topLevelBlock(yamlPath, "metadata:")
			if err != nil {
				return err
			}
			m := metaVersionRe.FindStringSubmatch(metaBlock)
			if m == nil {
				continue // 没有 version 字段的（理论上不该发生），跳过不纳入
			}

			var depIDs []string
			for _, key := range []string{"dependencies:", "shell:"} {
				block, err := topLevelBlock(yamlPath, key)
				if err != nil {
					return err
				}
				for _, ref := range depRefRe.FindAllStringSubmatch(block, -1) {
					depIDs = append(depIDs, ref[1])
				}
			}

			reg[id] = &Component{ID: id, Dir: dir, Version: m[1], DepIDs: depIDs}
		}
		return nil
	}
	componentsDir := filepath.Join(root, "components")
	if err := scan(componentsDir, componentDirs(componentsDir)); err != nil {
		return nil, err
	}
	shellDir := filepath.Join(root, "shell")
	if err := scan(shellDir, shellDirs(root)); err != nil {
		return nil, err
	}
	return reg, nil
}
