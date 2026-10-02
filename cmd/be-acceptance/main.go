package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

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
	case "bump-version":
		err = runBumpVersion(os.Args[2:])
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
	fmt.Println("  gate data-scope-test-scan --root <path>   声明了 data_scopes 维度的组件必须有越权/拒绝形状的测试（总纲 SOP-W-8）")
	fmt.Println("  gate dependency-version-scan --root <path> 外壳 go.mod 锁定版本、外壳 version 与 image tag 必须跟真实版本一致（brickkit up --dry-run 拦不住的两类；组件依赖引用/顶层 pin/shell.members 交给 brickkit up --dry-run）")
	fmt.Println("  gate service-hostname-scan --root <path>  config/*.yaml 与部署文件 vars: 里的版本化服务名必须与 brickkit.yaml 声明的版本一致（不一致=错误，未声明=警告）")
	fmt.Println("  gate config-key-scan     --root <path> [--strict]  组件与外壳 configSchema 的键必须是 ^[A-Z][A-Z0-9_]*$、不以 _ENDPOINT 结尾、不撞平台保留名（1.x 组件只警告，--strict 也判红）")
	fmt.Println("  gate openapi-additive-scan --root <path>  contracts/*.openapi.yaml 相对组件最近一次发布 tag 只增不删不改（决策 0302，buf 只管 .proto）")
	fmt.Println()
	fmt.Println("  bump-version --root <path> --plan <计划文件> [--apply]   自动传播一次版本变更（算出所有下游要跟着同步的组件，改好全部文件），计划文件格式见 versionbump 包文档")
}

// runGate 派发到具体门禁子命令。⚠️ 子命令词（如 "import-scan"）必须在
// fs.Parse 之前先摘掉——flag.Parse 遇到第一个非 flag 参数就停止解析，
// 见 tools/be-ops 同一个坑（docs/dev/实测踩坑记录.md C3）。
func runGate(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("用法：be-acceptance gate <import-scan|system-client-scan|bare-route-scan|events-breaking-scan|data-scope-test-scan|dependency-version-scan|service-hostname-scan|config-key-scan|openapi-additive-scan> --root <path>")
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet(sub, flag.ExitOnError)
	root := fs.String("root", ".", "装配仓库根目录")
	strict := fs.Bool("strict", false, "config-key-scan：还没迁到 2.x 的组件也判红")
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
	case "data-scope-test-scan":
		return runDataScopeTestScan(*root)
	case "dependency-version-scan":
		return runDependencyVersionScan(*root)
	case "service-hostname-scan":
		return runServiceHostnameScan(*root)
	case "config-key-scan":
		return runConfigKeyScan(*root, *strict)
	case "openapi-additive-scan":
		return runOpenAPIAdditiveScan(*root)
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

func runDataScopeTestScan(root string) error {
	gaps, err := gates.DataScopeTestScan(root)
	if err != nil {
		return err
	}
	if len(gaps) > 0 {
		for _, g := range gaps {
			fmt.Fprintf(os.Stderr, "✗ %s：声明了 data_scopes 维度 %s，但测试文件里找不到任何「越权/超出范围被拒绝」形状的测试（数据权限边界是业务规则的一部分，光测「范围内能看到」不够，总纲 SOP-W-8）\n",
				g.Component, strings.Join(g.Dimensions, "+"))
		}
		return fmt.Errorf("data-scope-test-scan 发现 %d 个组件缺失数据权限边界测试", len(gaps))
	}
	fmt.Println("✓ data-scope-test-scan：0 条违规")
	return nil
}

func runDependencyVersionScan(root string) error {
	mismatches, err := gates.DependencyVersionScan(root)
	if err != nil {
		return err
	}
	if len(mismatches) > 0 {
		for _, m := range mismatches {
			if strings.HasSuffix(m.Declarer, "go.mod") {
				fmt.Fprintf(os.Stderr, "✗ %s 声明依赖 %s@%s，但 %s 自己 component.yaml 里的真实版本是 %s——外壳镜像实际编译进去的是 go.mod 锁定的这个旧版本，brickkit up 不会校验、不会报错，合并部署的容器会悄悄服务旧代码（05b Task 4c）\n",
					m.Declarer, m.Dependency, m.DeclaredVersion, m.Dependency, m.ActualVersion)
				continue
			}
			fmt.Fprintf(os.Stderr, "✗ %s：%s 声明的版本 %s 与真实版本 %s 不一致——brickkit v1 的 lint 与 up --dry-run 都不检查这一项\n",
				m.Declarer, m.Dependency, m.DeclaredVersion, m.ActualVersion)
		}
		return fmt.Errorf("dependency-version-scan 发现 %d 条版本号漂移", len(mismatches))
	}
	fmt.Println("✓ dependency-version-scan：0 条违规")
	return nil
}

// runServiceHostnameScan：已声明组件的版本对不上是错误（非零退出）；brickkit.yaml 里根本没有的
// 组件只警告——项目还没装上它时这是预期状态。
func runServiceHostnameScan(root string) error {
	findings, err := gates.ServiceHostnameScan(root)
	if err != nil {
		return err
	}
	errCount, warnCount := 0, 0
	for _, f := range findings {
		if f.Error {
			errCount++
			fmt.Fprintf(os.Stderr, "✗ %s:%d：%s 与 brickkit.yaml 不一致——%s 声明的服务名是 %s。组件发版后地址没跟着改，运行期调用会落空（不报错，只是 503/403）\n",
				f.File, f.Line, f.Hostname, f.ComponentID, strings.Join(f.Expected, " / "))
			continue
		}
		warnCount++
		fmt.Fprintf(os.Stderr, "⚠ %s:%d：%s 对应的组件不在 brickkit.yaml 里，无法核对版本（还没装上它时是预期状态）\n",
			f.File, f.Line, f.Hostname)
	}
	if errCount > 0 {
		return fmt.Errorf("service-hostname-scan 发现 %d 处版本化服务名与 brickkit.yaml 不一致", errCount)
	}
	fmt.Printf("✓ service-hostname-scan：0 条错误（%d 条警告）\n", warnCount)
	return nil
}

// configKeyRuleText 是每条规则给人看的说明。
var configKeyRuleText = map[string]string{
	"naming":          "不满足 ^[A-Z][A-Z0-9_]*$——键就是注入进程的环境变量名，必须大写下划线",
	"endpoint-suffix": "以 _ENDPOINT 结尾——平台保留后缀，平台注入的值会覆盖它，组件永远拿不到自己的值",
	"reserved":        "撞了平台保留名（COMPONENT_ID / COMPONENT_VERSION / PORT / BRICKKIT_SERVED_MEMBERS / BRICKKIT_SERVED_MEMBERS_CONFIG）——平台的值获胜，只有一条警告",
}

// runConfigKeyScan：2.x 组件与全部外壳违规即判红；还在 1.x 的组件（06b 迁移中）
// 违规照样逐条打印，但只算警告——全部组件到 2.x 之后宽松名单自然为空，门禁就是严格的。
// --strict 现在就把 1.x 组件也算进失败。
func runConfigKeyScan(root string, strict bool) error {
	violations, err := gates.ConfigKeyScan(root)
	if err != nil {
		return err
	}
	errCount, warnCount := 0, 0
	// 1.x 组件的违规按清单归成一行，免得 make gates 被几十行警告淹没。
	var pendingFiles []string
	pendingKeys := map[string][]string{}
	for _, v := range violations {
		if v.Pending && !strict {
			warnCount++
			if _, seen := pendingKeys[v.File]; !seen {
				pendingFiles = append(pendingFiles, v.File)
			}
			pendingKeys[v.File] = append(pendingKeys[v.File], fmt.Sprintf("%s:%d[%s]", v.Key, v.Line, v.Rule))
			continue
		}
		errCount++
		fmt.Fprintf(os.Stderr, "✗ %s:%d：%s [%s] %s\n", v.File, v.Line, v.Key, v.Rule, configKeyRuleText[v.Rule])
	}
	for _, f := range pendingFiles {
		fmt.Fprintf(os.Stderr, "⚠ %s（组件还在 1.x，迁移到 2.x 时改名，现在不计入失败）：%s\n", f, strings.Join(pendingKeys[f], " "))
	}
	if errCount > 0 {
		return fmt.Errorf("config-key-scan 发现 %d 条违规", errCount)
	}
	if warnCount > 0 {
		fmt.Printf("✓ config-key-scan：0 条违规（另有 %d 个 1.x 组件的 %d 条违规只警告，--strict 判红；规则：naming=必须 ^[A-Z][A-Z0-9_]*$，endpoint-suffix=不许以 _ENDPOINT 结尾，reserved=不许撞平台保留名）\n", len(pendingFiles), warnCount)
		return nil
	}
	fmt.Println("✓ config-key-scan：0 条违规")
	return nil
}

func runOpenAPIAdditiveScan(root string) error {
	violations, notices, err := gates.OpenAPIAdditiveScan(root)
	if err != nil {
		return err
	}
	for _, n := range notices {
		fmt.Fprintf(os.Stderr, "ℹ %s\n", n)
	}
	if len(violations) > 0 {
		for _, v := range violations {
			fmt.Fprintf(os.Stderr, "✗ %s/%s（对比 %s）：%s [%s] %s（决策 0302：契约只增不删不改）\n",
				v.Component, v.File, v.BaseTag, v.Location, v.Rule, v.Detail)
		}
		return fmt.Errorf("openapi-additive-scan 发现 %d 条破坏性变更", len(violations))
	}
	fmt.Printf("✓ openapi-additive-scan：0 条违规（%d 条跳过提示）\n", len(notices))
	return nil
}
