package gates

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DependencyVersionMismatch 是一条依赖版本号漂移：Declarer 声明了要用
// Dependency 的 DeclaredVersion，但 Dependency 自己 component.yaml 里
// metadata.version 真实是 ActualVersion，两者不一致。
type DependencyVersionMismatch struct {
	Declarer        string // 声明方："组件 ID"，或字面量 "brickkit.yaml"
	Dependency      string // 被依赖的组件 ID，比如 "mdm/customer"
	DeclaredVersion string
	ActualVersion   string
}

// depRef 是从某处文本里抽出的一条"我要用 X 的哪个版本"引用。
type depRef struct {
	id      string
	version string
}

// 匹配 "scope/name@version" 这个子串——不区分它出现在纯字符串形式
// （`- mdm/customer@1.0.3`）还是对象形式
// （`- { id: infra/workflow@1.0.0, optional: true }`）里，两种形式里
// id@version 长得一样，直接在块文本上正则扫比先按 YAML 结构切开更省事
// （be-acceptance 目前零第三方依赖，不引入 YAML 库，同
// DataScopeTestScan 的既有技术选择）。
var depRefRe = regexp.MustCompile(`\b([a-z][a-z0-9_-]*/[a-z][a-z0-9_-]*)@([0-9]+\.[0-9]+\.[0-9]+)\b`)

// brickkit.yaml 顶层 `components:` 列表的两行固定形状：
//
//	  - id: mdm/customer
//	    version: 1.0.4 # 注释
var pinIDRe = regexp.MustCompile(`^-\s*id:\s*([a-z][a-z0-9_-]*/[a-z][a-z0-9_-]*)\s*$`)
var pinVersionRe = regexp.MustCompile(`^version:\s*([0-9]+\.[0-9]+\.[0-9]+)`)

// component.yaml 的 metadata 段里的 `version: 1.0.4  # 注释...`。
var metaVersionRe = regexp.MustCompile(`(?m)^\s*version:\s*(\S+)`)

// DependencyVersionScan 比对两类"声明版本 vs 真实版本"：
//
//  1. 每个组件 component.yaml 的 `dependencies.components` 列表项，跟
//     被依赖组件自己 component.yaml 的 `metadata.version` 比对；
//  2. `brickkit.yaml` 顶层 `components[].version` 这个顶层 pin，同样
//     跟对应组件自己 component.yaml 的 `metadata.version` 比对。
//
// 两类合起来才是 C16 那类坑的完整覆盖面（docs/dev/实测踩坑记录.md
// C16）——本仓库历史上这条坑至少复发过 4 次：有的是①（一个组件的版本
// 号改了，别的组件 `dependencies.components` 里引用它的那一行没跟着
// 改），有的是②（顶层 pin 落后于组件自己已经发布的新版本）。
//
// brickkit 对依赖版本号做逐字匹配（导读"平台的四条铁律"第 1 条：
// `1.2.0` 可以，`^1.2`/`latest` 一律不行），这两类漂移都会让
// `brickkit up --dry-run` 把同一个组件解析成两个独立节点，真实生成两
// 份迁移+启动指令——不是警告级别的坑，是真的会跑出重复资源。
//
// ⚠️ 判据只能验证"引用的组件在本仓库存在"这部分——引用了一个本仓库里
// 根本没有的组件 ID（拼写错误/组件被归档）时，这里查不出真实版本，
// 静默跳过那一条，不报违规（跟 W-6 人工评审分工：结构性的版本号漂移
// 交给这个门禁，组件 ID 本身对不对交给别处）。
func DependencyVersionScan(root string) ([]DependencyVersionMismatch, error) {
	componentsDir := filepath.Join(root, "components")
	compDirs := componentDirs(componentsDir)

	actual := map[string]string{} // 组件 ID -> 它自己 component.yaml 里的真实版本
	for _, compDir := range compDirs {
		id := componentID(componentsDir, compDir)
		v, err := ownVersion(filepath.Join(compDir, "component.yaml"))
		if err != nil {
			return nil, err
		}
		if v != "" {
			actual[id] = v
		}
	}

	var mismatches []DependencyVersionMismatch

	// ① 组件之间的依赖引用
	for _, compDir := range compDirs {
		declarer := componentID(componentsDir, compDir)
		refs, err := dependencyRefs(filepath.Join(compDir, "component.yaml"))
		if err != nil {
			return nil, err
		}
		mismatches = append(mismatches, diffRefs(declarer, refs, actual)...)
	}

	// ② brickkit.yaml 顶层 pin
	pins, err := brickkitYamlPins(filepath.Join(root, "brickkit.yaml"))
	if err != nil {
		return nil, err
	}
	mismatches = append(mismatches, diffRefs("brickkit.yaml", pins, actual)...)

	sort.Slice(mismatches, func(i, j int) bool {
		if mismatches[i].Declarer != mismatches[j].Declarer {
			return mismatches[i].Declarer < mismatches[j].Declarer
		}
		return mismatches[i].Dependency < mismatches[j].Dependency
	})
	return mismatches, nil
}

func diffRefs(declarer string, refs []depRef, actual map[string]string) []DependencyVersionMismatch {
	var out []DependencyVersionMismatch
	for _, ref := range refs {
		realVersion, ok := actual[ref.id]
		if !ok || realVersion == ref.version {
			continue
		}
		out = append(out, DependencyVersionMismatch{
			Declarer:        declarer,
			Dependency:      ref.id,
			DeclaredVersion: ref.version,
			ActualVersion:   realVersion,
		})
	}
	return out
}

// ownVersion 读 component.yaml 的 `metadata:` 段，返回 `version:` 的值。
func ownVersion(path string) (string, error) {
	block, err := topLevelBlock(path, "metadata:")
	if err != nil {
		return "", err
	}
	m := metaVersionRe.FindStringSubmatch(block)
	if m == nil {
		return "", nil
	}
	return m[1], nil
}

// dependencyRefs 从 component.yaml 的 `dependencies:` 段里抽取所有
// "scope/name@version" 引用。⚠️ 故意不先按 `components:`/`resources:`
// 子键切开再扫——`resources:` 段的条目（`{ kind: database, engine:
// postgresql }` 这种形状）里不会出现 "id@version" 子串，整块一起扫天然
// 不会误命中，比先做子键切分更省代码。
func dependencyRefs(path string) ([]depRef, error) {
	block, err := topLevelBlock(path, "dependencies:")
	if err != nil {
		return nil, err
	}
	return extractDepRefs(block), nil
}

// brickkitYamlPins 读 brickkit.yaml 顶层 `components:` 列表，把每个
// `- id: <组件ID>` 跟紧随其后的 `version: <版本>` 配成一对。
//
// ⚠️ 不能像 dependencyRefs 一样直接对整块正则扫"id@version"——顶层 pin
// 的 id 和 version 分写在两行（`- id: mdm/customer` 换行
// `version: 1.0.4`），没有 `@` 连接符，必须按"最近一个 id 之后第一个
// version"配对，逐行扫描。
func brickkitYamlPins(path string) ([]depRef, error) {
	block, err := topLevelBlock(path, "components:")
	if err != nil {
		return nil, err
	}
	var refs []depRef
	var currentID string
	for _, l := range strings.Split(block, "\n") {
		t := strings.TrimSpace(l)
		if m := pinIDRe.FindStringSubmatch(t); m != nil {
			currentID = m[1]
			continue
		}
		if currentID == "" {
			continue
		}
		if m := pinVersionRe.FindStringSubmatch(t); m != nil {
			refs = append(refs, depRef{id: currentID, version: m[1]})
			currentID = ""
		}
	}
	return refs, nil
}

func extractDepRefs(block string) []depRef {
	var refs []depRef
	for _, m := range depRefRe.FindAllStringSubmatch(block, -1) {
		refs = append(refs, depRef{id: m[1], version: m[2]})
	}
	return refs
}

// topLevelBlock 从"顶格 <key>"这一行开始，收集到下一个顶格行为止的
// 整块文本（含首行）。跟 DataScopeTestScan 的 dataScopeDimensions 用的
// 是同一种"没有 YAML 库时提取一个顶层块"技巧，这里单独成一份小函数是
// 因为要在 metadata:/dependencies:/components: 三处复用，没有借用既有
// 那份私有实现（保持每个 gate 文件自包含，同 eventsbreaking.go 等既有
// 文件的既有做法）。
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
