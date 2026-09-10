package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/brickKit/be-acceptance/gates"
)

// be-acceptance 是验收测试仓库，不是 brickKit 组件（总纲 §2.3）。
// 四个跨仓库门禁：铁律六 import 扫描（阶段一 Task 9）、SystemClient 误用
// 扫描 + 裸路由/裸 resolver 扫描（阶段二 Task 2 起造，阶段三 Task 3 把
// 后两条从 Go-only 扩展到 Python/TS——本阶段第一次出现这两种语言的
// 组件，判据必须跟上）、事件契约破坏性变更扫描（阶段三收官后补——
// buf breaking 只认 .proto，events/*.json 一直没有机器门禁，总纲 SOP-W-8）。
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
	fmt.Println("  gate import-scan          --root <path>   铁律六 import 扫描")
	fmt.Println("  gate system-client-scan   --root <path>   SystemClient 不许出现在用户请求路径上（Go/Python/TS）")
	fmt.Println("  gate bare-route-scan      --root <path>   业务代码不许裸注册路由/resolver（Go gin/Python FastAPI/TS resolver）")
	fmt.Println("  gate events-breaking-scan --root <path>   contracts/events/*.json 只增不删不改（§3.10，buf 只管 .proto）")
}

// runGate 派发到具体门禁子命令。⚠️ 子命令词（如 "import-scan"）必须在
// fs.Parse 之前先摘掉——flag.Parse 遇到第一个非 flag 参数就停止解析，
// 见 tools/be-ops 同一个坑（docs/dev/实测踩坑记录.md C3）。
func runGate(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("用法：be-acceptance gate <import-scan|system-client-scan|bare-route-scan|events-breaking-scan> --root <path>")
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet(sub, flag.ExitOnError)
	root := fs.String("root", ".", "装配仓库根目录")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	switch sub {
	case "import-scan":
		return runImportScan(*root)
	case "system-client-scan":
		return runSystemClientScan(*root)
	case "bare-route-scan":
		return runBareRouteScan(*root)
	case "events-breaking-scan":
		return runEventsBreakingScan(*root)
	default:
		return fmt.Errorf("门禁 %q 未知", sub)
	}
}

func runImportScan(root string) error {
	violations, err := gates.ImportScan(root)
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

func runSystemClientScan(root string) error {
	violations, err := gates.SystemClientScan(root)
	if err != nil {
		return err
	}
	if len(violations) > 0 {
		for _, v := range violations {
			fmt.Fprintf(os.Stderr, "✗ %s:%d：%s 在用户请求路径上用了 SystemClient（§14.2.6：数据权限被绕过，不报错，返回的数据只是「多了一些」）\n",
				v.File, v.Line, v.Component)
		}
		return fmt.Errorf("SystemClient 误用扫描发现 %d 条违规", len(violations))
	}
	fmt.Println("✓ SystemClient 误用扫描：0 条违规")
	return nil
}

func runBareRouteScan(root string) error {
	violations, err := gates.BareRouteScan(root)
	if err != nil {
		return err
	}
	if len(violations) > 0 {
		for _, v := range violations {
			fmt.Fprintf(os.Stderr, "✗ %s:%d：%s 裸注册了 %s，绕开了 besdk 的权限键强制（导读第 23 条）\n",
				v.File, v.Line, v.Component, v.Method)
		}
		return fmt.Errorf("裸路由/裸 resolver 扫描发现 %d 条违规", len(violations))
	}
	fmt.Println("✓ 裸路由/裸 resolver 扫描：0 条违规")
	return nil
}

func runEventsBreakingScan(root string) error {
	violations, err := gates.EventsBreakingScan(root)
	if err != nil {
		return err
	}
	if len(violations) > 0 {
		for _, v := range violations {
			fmt.Fprintf(os.Stderr, "✗ %s 的 %s：main 版本里的 `%s` 在当前版本里没了——删字段/改类型/删 subject 都是破坏性变更（§3.10、决策 19：只增不删不改）\n",
				v.Component, v.File, v.Missing)
		}
		return fmt.Errorf("事件契约破坏性变更扫描发现 %d 条违规", len(violations))
	}
	fmt.Println("✓ 事件契约破坏性变更扫描：0 条违规")
	return nil
}
