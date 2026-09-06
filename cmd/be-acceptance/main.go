package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/brickKit/be-acceptance/gates"
)

// be-acceptance 是验收测试仓库，不是 brickKit 组件（总纲 §2.3）。
// 目前只有一个门禁：铁律六 import 扫描（阶段一 Task 9）。
func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "gate":
		err = runGate(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "子命令 %q 未知\n", os.Args[1])
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "✗", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("be-acceptance —— BrickEnterprise 验收测试")
	fmt.Println()
	fmt.Println("  gate import-scan --root <path>   铁律六 import 扫描")
}

// runGate 派发到具体门禁子命令。⚠️ 子命令词（"import-scan"）必须在
// fs.Parse 之前先摘掉——flag.Parse 遇到第一个非 flag 参数就停止解析，
// 见 tools/be-ops 同一个坑（docs/dev/实测踩坑记录.md C3）。
func runGate(args []string) error {
	if len(args) < 1 || args[0] != "import-scan" {
		return fmt.Errorf("用法：be-acceptance gate import-scan --root <path>")
	}
	fs := flag.NewFlagSet("import-scan", flag.ExitOnError)
	root := fs.String("root", ".", "装配仓库根目录")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	violations, err := gates.ImportScan(*root)
	if err != nil {
		return err
	}
	if len(violations) > 0 {
		for _, v := range violations {
			fmt.Fprintf(os.Stderr, "✗ %s:%d：%s import 了 %s（铁律六：组件互不 import）\n",
				v.File, v.Line, v.From, v.To)
		}
		return fmt.Errorf("铁律六 import 扫描发现 %d 条违规", len(violations))
	}
	fmt.Println("✓ 铁律六 import 扫描：0 条违规")
	return nil
}
