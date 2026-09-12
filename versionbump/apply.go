package versionbump

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ApplyResult 是一条变更真正落地后的结果，供调用方打印摘要。
type ApplyResult struct {
	Change
	FilesChanged []string
}

// authzHostname 组件（`infra/authz`/`infra/iam-casdoor`）的版本号会被编进
// 容器主机名，进而被 13 个组件手写进 authzBundleUrl/iamJwksUrl 配置项
// 字面量（docs/dev/field-tested-pitfalls-log.md C18——dependency-version-
// scan 门禁扫不出这类漂移，2026 年真的漏改过全部 25 处引用）。这两个
// 组件的版本一变，这个包顺带机械同步这些字面量，把 C18 彻底堵死。
var authzHostnameComponents = map[string]string{
	"infra/authz":       "authzBundleUrl",
	"infra/iam-casdoor": "iamJwksUrl",
}

// hostnameFromVersion 把 "erp/inventory"+"1.0.12" 变成
// "erp-inventory-1-0-12"——brickkit 生成的容器名/主机名前缀的固定形状。
func hostnameFromVersion(id, version string) string {
	return strings.ReplaceAll(id, "/", "-") + "-" + strings.ReplaceAll(version, ".", "-")
}

// Apply 把 ComputeCascade 算出来的整批变更写进磁盘：每个组件自己的
// component.yaml + 根 brickkit.yaml 的顶层 pin + 根/docs/zh 两份
// AGENTS.md 的组件名录表 + （仅 infra/authz、infra/iam-casdoor 触发时）
// 全项目 authzBundleUrl/iamJwksUrl 字面量同步。dryRun=true 时只计算、
// 不写文件，返回值一致，方便调用方先过一遍人工审查。
func Apply(root string, reg map[string]*Component, changes []Change, dryRun bool) ([]ApplyResult, error) {
	newVerByID := map[string]string{}
	for _, c := range changes {
		newVerByID[c.ID] = c.NewVer
	}

	var results []ApplyResult
	for _, c := range changes {
		comp, ok := reg[c.ID]
		if !ok {
			return nil, fmt.Errorf("内部错误：变更引用的组件 %q 不在 registry 里", c.ID)
		}
		res := ApplyResult{Change: c}

		yamlPath := filepath.Join(comp.Dir, "component.yaml")
		changedFile, err := rewriteComponentYAML(yamlPath, c, newVerByID, dryRun)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", yamlPath, err)
		}
		if changedFile {
			res.FilesChanged = append(res.FilesChanged, yamlPath)
		}

		brickkitPath := filepath.Join(root, "brickkit.yaml")
		changedFile, err = rewriteBrickkitPin(brickkitPath, c, dryRun)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", brickkitPath, err)
		}
		if changedFile {
			res.FilesChanged = append(res.FilesChanged, brickkitPath)
		}

		for _, rosterPath := range []string{
			filepath.Join(root, "AGENTS.md"),
			filepath.Join(root, "docs", "zh", "AGENTS.md"),
		} {
			changedFile, err = rewriteRosterVersion(rosterPath, comp.RepoName(), c.NewVer, dryRun)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", rosterPath, err)
			}
			if changedFile {
				res.FilesChanged = append(res.FilesChanged, rosterPath)
			}
		}

		if configKey, ok := authzHostnameComponents[c.ID]; ok {
			oldHost := hostnameFromVersion(c.ID, c.OldVer)
			newHost := hostnameFromVersion(c.ID, c.NewVer)
			touched, err := syncHostnameLiteral(root, configKey, oldHost, newHost, dryRun)
			if err != nil {
				return nil, err
			}
			res.FilesChanged = append(res.FilesChanged, touched...)
		}

		results = append(results, res)
	}
	return results, nil
}

var versionLineTemplate = regexp.MustCompile(`(?m)^([ \t]*version:[ \t]*)([0-9]+\.[0-9]+\.[0-9]+)(.*)$`)
var imageLineTemplate = regexp.MustCompile(`(?m)^([ \t]*image:[ \t]*brickenterprise/[a-z0-9_-]+:)([0-9]+\.[0-9]+\.[0-9]+)([ \t]*)$`)

// rewriteComponentYAML 处理一个组件自己的 component.yaml：
//  1. version 那一行的版本号原地替换，这一行原有的行尾注释（不管是
//     固定的"精确版本，不接受…"说明，还是像 infra-print 那样已经把
//     历史变更记录写在同一行）完全不动；
//  2. 紧跟着插入一行新的变更记录，缩进对齐到原 version 行 `#` 出现的
//     列（没有 `#` 就退回一个默认列），不去动文件里任何其它字节；
//  3. deployment.image 的 tag 原地替换；
//  4. dependencies.components 段里，凡是引用到本批次里其它也变了版本
//     的组件（newVerByID 的 key），把 `id@旧版本` 换成 `id@新版本`——
//     只在 dependencies: 这一段内替换，不碰同一份文件里别处偶然出现的
//     同一个子串（比如变更记录叙述里提到过的旧版本号）。
func rewriteComponentYAML(path string, c Change, newVerByID map[string]string, dryRun bool) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	content := string(data)
	original := content

	loc := versionLineTemplate.FindStringSubmatchIndex(content)
	if loc == nil {
		return false, fmt.Errorf("找不到 version: 那一行")
	}
	versionLine := content[loc[0]:loc[1]]
	prefix := content[loc[2]:loc[3]]
	oldVerInFile := content[loc[4]:loc[5]]
	if oldVerInFile != c.OldVer {
		return false, fmt.Errorf("component.yaml 里的当前版本 %s 跟 registry 记的 %s 不一致，可能文件在扫描之后被改过，拒绝盲写", oldVerInFile, c.OldVer)
	}
	suffix := content[loc[6]:loc[7]]

	indent := strings.Index(versionLine, "#")
	if indent < 0 {
		indent = 38
	}
	newChangelogLine := fmt.Sprintf("%s# v%s：%s", strings.Repeat(" ", indent), c.NewVer, c.Reason)

	newVersionLine := prefix + c.NewVer + suffix
	content = content[:loc[0]] + newVersionLine + "\n" + newChangelogLine + content[loc[1]:]

	content = imageLineTemplate.ReplaceAllStringFunc(content, func(m string) string {
		sub := imageLineTemplate.FindStringSubmatch(m)
		return sub[1] + c.NewVer + sub[3]
	})

	content, err = rewriteDependencyRefs(content, newVerByID)
	if err != nil {
		return false, err
	}

	if content == original {
		return false, nil
	}
	if dryRun {
		return true, nil
	}
	return true, os.WriteFile(path, []byte(content), 0o644)
}

// rewriteDependencyRefs 只在 dependencies: 顶层块内，把
// "id@任意版本号"（id 是 newVerByID 的 key）替换成 "id@新版本"。
func rewriteDependencyRefs(content string, newVerByID map[string]string) (string, error) {
	lines := strings.Split(content, "\n")
	start, end := topLevelBlockRange(lines, "dependencies:")
	if start < 0 {
		return content, nil
	}
	for i := start; i < end; i++ {
		for id, newVer := range newVerByID {
			re := regexp.MustCompile(regexp.QuoteMeta(id) + `@[0-9]+\.[0-9]+\.[0-9]+`)
			lines[i] = re.ReplaceAllString(lines[i], id+"@"+newVer)
		}
	}
	return strings.Join(lines, "\n"), nil
}

// topLevelBlockRange 是 topLevelBlock 的"给行号范围"版本——apply.go 需要
// 定位行区间去做替换，scan.go 的 topLevelBlock 只返回拼好的文本。
func topLevelBlockRange(lines []string, key string) (start, end int) {
	start = -1
	for i, l := range lines {
		if strings.HasPrefix(l, key) {
			start = i
			break
		}
	}
	if start < 0 {
		return -1, -1
	}
	end = len(lines)
	for i := start + 1; i < len(lines); i++ {
		l := lines[i]
		if strings.TrimSpace(l) == "" {
			continue
		}
		if !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "\t") {
			end = i
			break
		}
	}
	return start, end
}

// rewriteBrickkitPin 处理根 brickkit.yaml 顶层 `components:` 列表里
// `- id: <ID>` 紧跟着的 `version:` 那一行。这份文件的既有习惯跟
// component.yaml 不同：version 行的行尾注释本身就是最新一条变更记录
// （没有单独的"精确版本"说明文字），所以新版本落地时把旧的那一整条
// 挪到下一行变成纯注释，而不是像 component.yaml 那样在下面另起一行。
func rewriteBrickkitPin(path string, c Change, dryRun bool) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	lines := strings.Split(string(data), "\n")

	idLineRe := regexp.MustCompile(`^\s*-\s*id:\s*` + regexp.QuoteMeta(c.ID) + `\s*$`)
	verLineRe := regexp.MustCompile(`^([ \t]*version:[ \t]*)([0-9]+\.[0-9]+\.[0-9]+)([ \t]*)(#.*)?$`)

	idLine := -1
	for i, l := range lines {
		if idLineRe.MatchString(l) {
			idLine = i
			break
		}
	}
	if idLine < 0 {
		return false, nil // brickkit.yaml 没有 pin 这个组件（比如它不在当前装配里），不是错误
	}

	verLine := -1
	for i := idLine + 1; i < len(lines) && i < idLine+5; i++ {
		if verLineRe.MatchString(lines[i]) {
			verLine = i
			break
		}
	}
	if verLine < 0 {
		return false, fmt.Errorf("brickkit.yaml 里 %s 的 id 行后面找不到 version: 行", c.ID)
	}

	m := verLineRe.FindStringSubmatch(lines[verLine])
	indentPrefix := lines[verLine][:len(lines[verLine])-len(strings.TrimLeft(lines[verLine], " \t"))]
	oldComment := m[4] // 含开头的 "#"，可能为空（第一次给这个组件打 pin 时还没有历史记录）

	newLine := indentPrefix + "version: " + c.NewVer + " # v" + c.NewVer + "：" + c.Reason
	insertedLines := []string{newLine}
	if oldComment != "" {
		insertedLines = append(insertedLines, indentPrefix+"# "+strings.TrimSpace(strings.TrimPrefix(oldComment, "#")))
	}

	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:verLine]...)
	out = append(out, insertedLines...)
	out = append(out, lines[verLine+1:]...)
	newContent := strings.Join(out, "\n")

	if dryRun {
		return true, nil
	}
	return true, os.WriteFile(path, []byte(newContent), 0o644)
}

// rewriteRosterVersion 改 AGENTS.md/docs/zh/AGENTS.md 组件名录表那一行
// 的版本号单元格。表里用的是仓库名（"erp-inventory"），不是组件 ID。
func rewriteRosterVersion(path, repoName, newVer string, dryRun bool) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	re := regexp.MustCompile(`(?m)^(\| ` + "`" + regexp.QuoteMeta(repoName) + "`" + ` \| )[0-9]+\.[0-9]+\.[0-9]+( \|)`)
	if !re.Match(data) {
		return false, nil // 这份名录表里没有这一行（比如工具仓库不在这张表里），不是错误
	}
	newContent := re.ReplaceAllString(string(data), "${1}"+newVer+"${2}")
	if dryRun {
		return true, nil
	}
	return true, os.WriteFile(path, []byte(newContent), 0o644)
}

// syncHostnameLiteral 把全项目 component.yaml 的 config: 段里，指定
// 配置键（authzBundleUrl/iamJwksUrl）值字符串里出现的旧主机名子串换成
// 新主机名——只替换这两个键的取值，不做全文件无差别替换（避免动到
// 别处偶然提到旧版本号的叙述文字）。
func syncHostnameLiteral(root, configKey, oldHost, newHost string, dryRun bool) ([]string, error) {
	var touched []string
	lineRe := regexp.MustCompile(`^(\s*` + regexp.QuoteMeta(configKey) + `:\s*").*(")\s*(#.*)?$`)
	oldHostRe := regexp.MustCompile(regexp.QuoteMeta(oldHost))

	var candidates []string
	compDirs := componentDirs(filepath.Join(root, "components"))
	for _, dir := range compDirs {
		candidates = append(candidates, filepath.Join(dir, "component.yaml"))
	}
	candidates = append(candidates, filepath.Join(root, "brickkit.yaml"))

	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		lines := strings.Split(string(data), "\n")
		changed := false
		for i, l := range lines {
			if !lineRe.MatchString(l) {
				continue
			}
			if oldHostRe.MatchString(l) {
				lines[i] = oldHostRe.ReplaceAllString(l, newHost)
				changed = true
			}
		}
		if !changed {
			continue
		}
		touched = append(touched, path)
		if dryRun {
			continue
		}
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			return nil, err
		}
	}
	return touched, nil
}
