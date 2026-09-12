package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/brickKit/be-acceptance/versionbump"
)

// runBumpVersion 是"每个改动都跳版本号"这条纪律的自动化落地——见
// 01-documentation-standard.md 附近的 SOP-W-11（新增文档小节）：本来每次
// 改完代码/测试/文档后，都要挨个去查有哪些组件引用了它、brickkit.yaml
// 的顶层 pin、两份 AGENTS.md 名录表分别在哪一行，手改一遍——这正是
// 01-testing-standard.md §1"能机制化的判据必须机制化"这条原则该管的
// 那类事，只是这次机制化的不是"检查"，是"传播"本身。
//
// 用法：
//
//	be-acceptance bump-version --root <path> --plan <计划文件>          # 只算、只打印，不写盘
//	be-acceptance bump-version --root <path> --plan <计划文件> --apply  # 真的写盘
//
// 计划文件格式见 versionbump.ParsePlanFile 的注释——只需要写"真的动了
// 什么的根组件"，因为依赖它们而要跟着同步版本号的下游组件由
// versionbump.ComputeCascade 自动算出来。
//
// ⚠️ 这个子命令只落地"文件层面"的版本传播（component.yaml/
// brickkit.yaml/两份 AGENTS.md 名录表），不做 git commit/tag/`make
// image`/push——那几步仍然逐个组件手动确认着做，理由见
// 01-documentation-standard.md 附近新增小节："写文件"是可以安全批量
// 自动化的机械操作，"推到远端"是这个项目一贯认定需要人在场确认的
// 有风险动作，两者不该被同一次调用捆在一起。
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
		fmt.Println()
	}

	if !*apply {
		fmt.Println("以上是计划——确认没问题后加 --apply 重跑一遍才会真的写文件。")
	} else {
		fmt.Println("文件已落地。接下来逐个组件（按上面打印的顺序）review diff、跑测试、" +
			"git commit/tag/`make image`/push，最后提交装配仓库自己的改动——" +
			"这几步这个工具不代劳，见 01-documentation-standard.md 新增小节的理由。")
	}
	return nil
}
