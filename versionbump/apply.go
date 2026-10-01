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

// Apply 把 ComputeCascade 算出来的整批变更写进磁盘：每个组件/外壳自己的
// component.yaml（version、变更记录、image tag、dependencies.components
// 与 shell.members 里的引用）+ 全部外壳 go.mod 里对应成员的 require。
// dryRun=true 时只计算、不写文件，返回值一致，方便调用方先过一遍人工审查。
func Apply(root string, reg map[string]*Component, changes []Change, dryRun bool) ([]ApplyResult, error) {
	newVerByID := map[string]string{}
	for _, c := range changes {
		newVerByID[c.ID] = c.NewVer
	}

	var goMods []string
	for _, dir := range shellDirs(root) {
		p := filepath.Join(dir, "go.mod")
		if _, err := os.Stat(p); err == nil {
			goMods = append(goMods, p)
		}
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

		for _, goMod := range goMods {
			changedFile, err := rewriteShellGoMod(goMod, comp.RepoName(), c.NewVer, dryRun)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", goMod, err)
			}
			if changedFile {
				res.FilesChanged = append(res.FilesChanged, goMod)
			}
		}

		results = append(results, res)
	}
	return results, nil
}

var versionLineTemplate = regexp.MustCompile(`(?m)^([ \t]*version:[ \t]*)([0-9]+\.[0-9]+\.[0-9]+)(.*)$`)

// imageLineRe 匹配 deployment.image 的 `image: <任意镜像仓库路径>:<版本>` 一行。
// v1 里 image 可以不写（只写 deployment.build），也可以是任意 registry——
// 所以不再写死前缀；是否改写由调用方按"tag 等于旧版本"判断，第三方镜像
// （tag 本来就跟组件版本无关）不会被误改，没有 image 行时一行都不动。
var imageLineRe = regexp.MustCompile(`(?m)^([ \t]*image:[ \t]*[^\s:#]+(?::[0-9]+)?(?:/[^\s:#]+)*:)([0-9]+\.[0-9]+\.[0-9]+)([ \t]*)$`)

// rewriteComponentYAML 处理一个组件自己的 component.yaml：
//  1. version 那一行的版本号原地替换，这一行原有的行尾注释完全不动；
//     不插入、不修改任何注释行——component.yaml 不承载历史（历史在 git 与
//     tag 的发布说明里），计划文件里的 reason 只用于终端打印和 tag 消息，
//     不写进文件；
//  2. deployment.image 的 tag 等于旧版本时原地替换（没有 image 行就什么
//     都不做）；
//  3. dependencies.components 段与外壳的 shell.members 段里，凡是引用到
//     本批次里其它也变了版本的组件（newVerByID 的 key），把 `id@旧版本`
//     换成 `id@新版本`——只在这两段内替换，不碰同一份文件里别处偶然出现的
//     同一个子串。
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
	prefix := content[loc[2]:loc[3]]
	oldVerInFile := content[loc[4]:loc[5]]
	if oldVerInFile != c.OldVer {
		return false, fmt.Errorf("component.yaml 里的当前版本 %s 跟 registry 记的 %s 不一致，可能文件在扫描之后被改过，拒绝盲写", oldVerInFile, c.OldVer)
	}
	suffix := content[loc[6]:loc[7]]

	content = content[:loc[0]] + prefix + c.NewVer + suffix + content[loc[1]:]

	content = imageLineRe.ReplaceAllStringFunc(content, func(m string) string {
		sub := imageLineRe.FindStringSubmatch(m)
		if sub[2] != c.OldVer {
			return m
		}
		return sub[1] + c.NewVer + sub[3]
	})

	// dependencies.components 与外壳的 shell.members 都是 id@version 引用。
	for _, key := range []string{"dependencies:", "shell:"} {
		content, err = rewriteRefsInBlock(content, key, newVerByID)
		if err != nil {
			return false, err
		}
	}

	if content == original {
		return false, nil
	}
	if dryRun {
		return true, nil
	}
	return true, os.WriteFile(path, []byte(content), 0o644)
}

// rewriteRefsInBlock 只在 key（dependencies: / shell:）顶层块内，把
// "id@任意版本号"（id 是 newVerByID 的 key）替换成 "id@新版本"。
func rewriteRefsInBlock(content, key string, newVerByID map[string]string) (string, error) {
	lines := strings.Split(content, "\n")
	start, end := topLevelBlockRange(lines, key)
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

// shellGoModLineRe 匹配 go.mod require 里一行顶层成员依赖（不含 /gen/ 契约
// 子模块、不含 // indirect）：
//
//	\tgithub.com/brickKit/erp-inventory/v2 v2.0.12
//	\tgithub.com/brickKit/erp-inventory v1.0.12
//
// 捕获组：1=缩进+仓库名前缀（含 github.com/brickKit/<repo>），2=可选的
// /vN 路径后缀，3=版本号。
func shellGoModLineRe(repoName string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^([ \t]*github\.com/brickKit/` + regexp.QuoteMeta(repoName) + `)(/v[0-9]+)?([ \t]+v)[0-9]+\.[0-9]+\.[0-9]+([ \t]*)$`)
}

// rewriteShellGoMod 改外壳 go.mod 里某个成员模块的 require 行：版本号
// 跟着变，Go 的语义化导入版本规则要求 v2+ 的模块路径带 /vN 后缀（v0/v1
// 不带），所以大版本变了路径后缀也一并改。
func rewriteShellGoMod(path, repoName, newVer string, dryRun bool) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	re := shellGoModLineRe(repoName)
	if !re.Match(data) {
		return false, nil // 这个外壳没有编进这个成员，不是错误
	}
	major := newVer
	if i := strings.Index(newVer, "."); i >= 0 {
		major = newVer[:i]
	}
	suffix := ""
	if major != "0" && major != "1" {
		suffix = "/v" + major
	}
	// 注意 go.mod 里 // indirect 行也可能命中——外壳自己声明的直接依赖
	// 行尾不带注释，正则末尾 `[ \t]*$` 已经把带 // indirect 的行排除了。
	newContent := re.ReplaceAllString(string(data), "${1}"+suffix+"${3}"+newVer+"${4}")
	if newContent == string(data) {
		return false, nil
	}
	if dryRun {
		return true, nil
	}
	return true, os.WriteFile(path, []byte(newContent), 0o644)
}
