package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/brickKit/be-acceptance/versionbump"
)

// runBumpVersion 是"每个改动都跳版本号"这条纪律的自动化落地：本来每次改完代码/测试/文档后，
// 都要挨个去查有哪些组件/外壳引用了它（下游 component.yaml 的依赖版本、外壳 shell.members、
// 外壳 go.mod 的 require），手改一遍——这里把"传播"本身机制化。
//
// 用法：
//
//	be-acceptance bump-version --root <path> --plan <计划文件>          # 只算、只打印，不写盘
//	be-acceptance bump-version --root <path> --plan <计划文件> --apply  # 真的写盘
//
// 计划文件格式见 versionbump.ParsePlanFile 的注释——只需要写"真的动了
// 什么的根组件"，因为依赖它们而要跟着同步版本号的下游组件由
// versionbump.ComputeCascade 自动算出来。计划里的 reason 只在这里原样打印，
// 不写进任何文件；发布说明由人手写。
//
// ⚠️ brickkit.yaml 顶层 pin 交给 `brickkit upgrade`、config: 主机名字面量交给
// `$var:`（由 dependency-version-scan 的主机名检查兜底）、AGENTS 名册交给 CLI 维护块，
// 这个子命令都不碰。
//
// ⚠️ 这个子命令只落地"文件层面"的版本传播，不做 git commit/push、`brickkit release`、
// `brickkit build`——"写文件"可以安全批量自动化，"推到远端/打 tag"需要人在场逐个确认，
// 两者不捆在同一次调用里。--apply 之后只打印每个组件接下来该做的步骤（nextSteps）。
func runBumpVersion(args []string) error {
	fs := flag.NewFlagSet("bump-version", flag.ExitOnError)
	root := fs.String("root", ".", "装配仓库根目录")
	planPath := fs.String("plan", "", "计划文件路径（必填）")
	apply := fs.Bool("apply", false, "真的写盘；不给这个 flag 只打印计划，不动任何文件")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *planPath == "" {
		return fmt.Errorf("用法：be-acceptance bump-version --root <path> --plan <计划文件> [--apply]")
	}

	planData, err := os.ReadFile(*planPath)
	if err != nil {
		return fmt.Errorf("读计划文件：%w", err)
	}
	seeds, err := versionbump.ParsePlanFile(string(planData))
	if err != nil {
		return fmt.Errorf("计划文件格式不对：%w", err)
	}

	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	reg, err := versionbump.LoadRegistry(absRoot)
	if err != nil {
		return fmt.Errorf("扫描组件：%w", err)
	}

	changes, err := versionbump.ComputeCascade(reg, seeds)
	if err != nil {
		return fmt.Errorf("算级联：%w", err)
	}

	results, err := versionbump.Apply(absRoot, reg, changes, !*apply)
	if err != nil {
		return fmt.Errorf("落地：%w", err)
	}

	mode := "计划（未写盘，加 --apply 才会真的改文件）"
	if *apply {
		mode = "已落地"
	}
	fmt.Printf("%s：\n\n", mode)
	// 一个文件可能记在别的组件名下（成员升版本时改外壳 go.mod 记在成员那条结果里），
	// 所以每个组件要 git add 的文件按目录从全部结果里收。
	var allFiles []string
	for _, r := range results {
		allFiles = append(allFiles, r.FilesChanged...)
	}
	for _, r := range results {
		kind := "根变更"
		if r.IsCascade {
			kind = "依赖同步"
		}
		fmt.Printf("  %s：%s → %s（%s）\n", r.ID, r.OldVer, r.NewVer, kind)
		fmt.Printf("    理由：%s\n", r.Reason)
		for _, f := range r.FilesChanged {
			rel, relErr := filepath.Rel(absRoot, f)
			if relErr != nil {
				rel = f
			}
			fmt.Printf("    - %s\n", rel)
		}
		if *apply {
			comp := reg[r.ID]
			for _, line := range nextSteps(absRoot, comp, r.NewVer, filesUnder(comp.Dir, allFiles)) {
				fmt.Println(line)
			}
		}
		fmt.Println()
	}

	if !*apply {
		fmt.Println("以上是计划——确认没问题后加 --apply 重跑一遍才会真的写文件。")
	} else {
		fmt.Println(applyFooter)
	}
	return nil
}

// applyFooter 是 --apply 之后的收尾提示。
const applyFooter = "文件已落地。按上面打印的顺序逐个组件收尾（先被依赖者，后依赖者）；全部做完后对每个发布过的组件跑 " +
	"`brickkit upgrade <id>@<新版本>`、跑 make gates 与 make version-check、真机验证、提交并推送装配仓库自己的改动。" +
	"完整流程、什么时候该停下来问人，见 .claude/skills/version-bump-ship/SKILL.md 与 " +
	"docs/conventions/development-workflow.md（Releasing）。"

// nextSteps 按 v1 发布规则打印一个组件/外壳在 --apply 之后的收尾步骤：
//   - 组件：提交并推送 → `brickkit release --notes-file`（tag `<ver>`，不带 v，注解 tag，自动推送）
//     → Go 组件在同一提交上再打 `v<ver>`（Go 模块代理只认 v 前缀）→ `brickkit build <id>`；
//   - 外壳（住在装配仓库的 shell/<scope>/<name>/）：在装配仓库根提交并推送 →
//     `brickkit release --path shell/<scope>/<name>`（tag `<scope>-<name>/<ver>`）→ `brickkit build`。
//     外壳不被任何人 import，装配仓库上绝不打裸 `v` 标签。
//
// 不打印 docker push：现阶段镜像全部本地使用。files 是相对组件目录的、bump-version 改过的文件。
func nextSteps(absRoot string, comp *versionbump.Component, newVer string, files []string) []string {
	compRel, err := filepath.Rel(absRoot, comp.Dir)
	if err != nil {
		compRel = comp.Dir
	}
	compRel = filepath.ToSlash(compRel)
	const notes = "<发布说明文件>"
	const notesHint = "      # 发布说明手写成一个文件（Markdown，只写上一个 tag 之后的变更），放在组件目录之外"

	if strings.HasPrefix(compRel, "shell/") {
		var paths []string
		for _, f := range files {
			paths = append(paths, compRel+"/"+filepath.ToSlash(f))
		}
		tag := strings.Replace(strings.TrimPrefix(compRel, "shell/"), "/", "-", 1) + "/" + newVer
		return []string{
			"    接下来（外壳，在装配仓库根目录下）：",
			"      跑外壳自己的构建/测试，必须全绿才能往下走",
			fmt.Sprintf("      git add %s <这次真正改动涉及的其它外壳文件>   # 只 add 自己的路径", strings.Join(paths, " ")),
			fmt.Sprintf("      git commit -F <提交信息文件> -- %s && git log --oneline -1", compRel),
			"      git push origin main",
			notesHint,
			fmt.Sprintf("      brickkit release --path %s --notes-file %s   # tag %s，注解 tag，自动推送", compRel, notes, tag),
			"      # 外壳不被 import：不要在装配仓库打裸 v 标签",
			fmt.Sprintf("      brickkit build %s   # 已有镜像会跳过，改了代码加 --force", comp.ID),
		}
	}

	lines := []string{
		fmt.Sprintf("    接下来（在 %s 目录下，先加上这次真正改的代码/测试文件，再照下面收尾）：", compRel),
		"      跑这个组件自己的测试，必须全绿才能往下走",
		fmt.Sprintf("      git add %s <这次真正改动涉及的其它文件>", strings.Join(files, " ")),
		"      git commit -F <提交信息文件> && git log --oneline -1   # 别用内联 -m 夹带长文本/双引号",
		"      git push origin main   # 发布检查要求被打 tag 的提交已在远端",
		notesHint,
		fmt.Sprintf("      brickkit release --notes-file %s   # tag %s（不带 v），注解 tag，自动推送", notes, newVer),
	}
	if isGoComponent(comp.Dir) {
		lines = append(lines,
			fmt.Sprintf("      git tag -a v%s -F %s && git push origin v%s   # Go 组件：同一提交再打 v 前缀 tag", newVer, notes, newVer))
	}
	lines = append(lines, fmt.Sprintf("      brickkit build %s   # 已有镜像会跳过，改了代码加 --force", comp.ID))
	return lines
}

// filesUnder 返回 files 里位于 dir 之下的那些，路径相对 dir，去重且保持首次出现的顺序。
func filesUnder(dir string, files []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range files {
		if !strings.HasPrefix(f, dir+string(filepath.Separator)) {
			continue
		}
		rel, err := filepath.Rel(dir, f)
		if err != nil || seen[rel] {
			continue
		}
		seen[rel] = true
		out = append(out, rel)
	}
	return out
}

// isGoComponent：组件根目录有 go.mod 即 Go 组件（v1 布局的 Go 组件模块在仓库根）。
func isGoComponent(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "go.mod"))
	return err == nil
}
