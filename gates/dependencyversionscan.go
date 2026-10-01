package gates

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DependencyVersionMismatch 是一条版本号漂移：Declarer 声明了要用
// Dependency 的 DeclaredVersion，但 Dependency 自己真实版本是
// ActualVersion，两者不一致。
type DependencyVersionMismatch struct {
	Declarer        string // 声明方："shell/be/<name>/go.mod" 或外壳 component.yaml 的相对路径
	Dependency      string // 被依赖的组件 ID，比如 "mdm/customer"
	DeclaredVersion string
	ActualVersion   string
}

// depRef 是从某处文本里抽出的一条"我要用 X 的哪个版本"引用。
type depRef struct {
	id      string
	version string
}

// component.yaml 的 metadata 段里的 `version: 1.0.4  # 注释...`。
var metaVersionRe = regexp.MustCompile(`(?m)^\s*version:\s*(\S+)`)

// shellGoModRe 匹配 go.mod `require` 块里的一行顶层依赖引用：
//
//	github.com/brickKit/mdm-customer/v2 v2.0.7
//	github.com/brickKit/mdm-customer v1.0.7
//
// v2 及以上的模块路径必须带 /vN 后缀（Go 的语义化导入版本规则），v0/v1
// 不带，所以后缀可选。故意不匹配 github.com/brickKit/mdm-customer/gen/mdm/customer
// 这类嵌套契约包（路径带 /gen/，后缀位置之后还有别的路径段，天然不被
// 本正则命中）——那份版本号是它自己独立发布节奏（设计书 §13.3 铁律六
// 第二类白名单），跟父模块版本号本来就不需要一致。`// indirect`
// 传递依赖另外过滤掉——那些不是外壳自己声明的直接依赖。
var shellGoModRe = regexp.MustCompile(`^\s*github\.com/brickKit/([a-z][a-z0-9-]*)(?:/v[0-9]+)?\s+v([0-9]+\.[0-9]+\.[0-9]+)\s*$`)

// DependencyVersionScan 比对两类 brickkit v1 自己拦不住的版本漂移：
//
//  1. `shell/be/<name>/go.mod` 锁定编译的各组件版本，跟对应组件自己
//     component.yaml 的 `metadata.version` 比对——外壳镜像实际编译进去
//     的是这里锁定的版本（05b Task 4c 真机验证：全部 11 个真实 Go 成员
//     都已经漂移）。go.mod 不属于 brickkit 的任何一层文件，v1 不读也不校验；
//  2. 外壳自己 component.yaml 的 `metadata.version` 与 `deployment.image`
//     镜像 tag 是否一致——两个字段各自独立声明，v1 的 lint 和
//     up --dry-run 实测都不查（实验记录 dev/test-records/06a/
//     task10-version-checks-experiment.md）。
//
// 原先还有两类——组件间 `dependencies.components` 引用、`brickkit.yaml`
// 顶层 pin——以及 shell.members，v1 的 `brickkit up --dry-run` 都会拦下
// 并给出可操作的提示（同一份实验记录），不再在这里重复检查，
// `make gates` 改为同时跑 `brickkit up --dry-run`。
//
// ⚠️ 判据只能验证"引用的组件在本仓库存在"这部分——引用了一个本仓库里
// 根本没有的组件时，这里查不出真实版本，静默跳过那一条，不报违规。
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

	// ① 外壳 go.mod 锁定的版本
	idsByRepoName := map[string]string{}
	for id := range actual {
		idsByRepoName[repoNameOf(id)] = id
	}
	shellGoMods, err := shellGoModPaths(root)
	if err != nil {
		return nil, err
	}
	for _, p := range shellGoMods {
		refs, err := shellModuleRefs(p, idsByRepoName)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			rel = p
		}
		mismatches = append(mismatches, diffRefs(filepath.ToSlash(rel), refs, actual)...)
	}

	// ② 外壳自己 component.yaml 的 metadata.version 与 deployment.image tag
	shellComponentYAMLs, err := shellComponentYAMLPaths(root)
	if err != nil {
		return nil, err
	}
	for _, p := range shellComponentYAMLs {
		mismatch, err := shellImageVersionMismatch(p, root)
		if err != nil {
			return nil, err
		}
		if mismatch != nil {
			mismatches = append(mismatches, *mismatch)
		}
	}

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

// repoNameOf 把 "erp/inventory" 变成 "erp-inventory"——go.mod 里的模块
// 路径、AGENTS.md 组件名录表、brickkit up 生成的容器名前缀，用的都是这个
// 形状。跟 versionbump.Component.RepoName() 是同一个变换，这里独立实现
// 一份而不是跨包借用（本文件顶部注释：每个 gate 文件自包含）。
func repoNameOf(componentID string) string {
	return strings.ReplaceAll(componentID, "/", "-")
}

// shellGoModPaths 找出全部 shell/be/<name>/go.mod（v1 布局：外壳是项目代码，
// 住在装配仓库的 shell/be/<name>/；Python 外壳用的不是 go.mod，天然不会被
// 这个 glob 命中）。
func shellGoModPaths(root string) ([]string, error) {
	return filepath.Glob(filepath.Join(root, "shell", "be", "*", "go.mod"))
}

// shellModuleRefs 从一份 shell/be/<name>/go.mod 里抽取全部顶层组件依赖引用，
// 返回的 depRef.id 已经是 componentId 形式（用 idsByRepoName 把 go.mod
// 里的仓库名转回 "scope/name"）——查不到对应仓库名的组件（比如
// be-sdk-go 这类不是业务组件的工具仓库）静默跳过，同本文件其它三类扫描
// 一致的"查不到就不报"判据。
func shellModuleRefs(path string, idsByRepoName map[string]string) ([]depRef, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var refs []depRef
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, "// indirect") || strings.Contains(line, "/gen/") {
			continue
		}
		m := shellGoModRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		id, ok := idsByRepoName[m[1]]
		if !ok {
			continue
		}
		refs = append(refs, depRef{id: id, version: m[2]})
	}
	return refs, nil
}

// shellComponentYAMLPaths 找出全部 shell/be/<name>/component.yaml
// （外壳自己的 manifest，不是 components/ 下的业务组件）。
func shellComponentYAMLPaths(root string) ([]string, error) {
	return filepath.Glob(filepath.Join(root, "shell", "be", "*", "component.yaml"))
}

// deploymentImageTagRe 匹配 `deployment:` 段里 `image: <repo>:<version>` 这一行，
// 只取冒号后面的精确版本号。
var deploymentImageTagRe = regexp.MustCompile(`(?m)^\s*image:\s*\S+:([0-9]+\.[0-9]+\.[0-9]+)\s*$`)

// shellMetaIDRe 匹配 `metadata:` 段里的 `id: be/go-core`。
var shellMetaIDRe = regexp.MustCompile(`(?m)^\s*id:\s*(\S+)`)

// shellImageVersionMismatch 检查一份外壳 component.yaml 自己的
// metadata.version 是否与 deployment.image 的镜像 tag 一致——查不到任何一边
// 就静默跳过（同本文件其它扫描"查不到就不报"的既有判据）。
func shellImageVersionMismatch(path, root string) (*DependencyVersionMismatch, error) {
	metaBlock, err := topLevelBlock(path, "metadata:")
	if err != nil {
		return nil, err
	}
	vm := metaVersionRe.FindStringSubmatch(metaBlock)
	if vm == nil {
		return nil, nil
	}
	version := vm[1]

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	im := deploymentImageTagRe.FindStringSubmatch(string(data))
	if im == nil {
		return nil, nil
	}
	imageTag := im[1]

	if version == imageTag {
		return nil, nil
	}

	id := "?"
	if idm := shellMetaIDRe.FindStringSubmatch(metaBlock); idm != nil {
		id = idm[1]
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	return &DependencyVersionMismatch{
		Declarer:        rel,
		Dependency:      id + " 自己的 deployment.image",
		DeclaredVersion: version,
		ActualVersion:   imageTag,
	}, nil
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
