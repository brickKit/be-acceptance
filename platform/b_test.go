// Group B · 依赖注入族（用例 3、4、5、8）——本阶段的主线成果：
// erp-sales 是全阶段二唯一有真实依赖方的组件，用例 3/4/5 阶段一根本
// 没有验证前提（mdm-customer 那时没有任何依赖方），是本阶段第一次
// 具备条件。
//
// ⚠️ 用例 3/4/5 直接对着 `brickkit up` 真实起的五组件部署（Task 18）
// 做 docker exec 断言——不搭临时 fixture，因为断言本身就是"erp-sales
// 这个真实容器里的环境变量长什么样"，跟 closedloop 包对 mdm-customer
// 的既有判据一致：需要真实外部环境，环境不在就跳过，不是放宽断言。
// 用例 8 涉及"enabled: false 会级联"这条通用机制，不是 erp-sales 独有，
// 用隔离的临时 fixture 验（不去扰动真实跑着的五组件部署）。
package platform_test

import (
	"os/exec"
	"strings"
	"testing"
)

const erpSalesContainer = "brickkit-be-assembly-standard-erp-sales-1-0-1-1"

// dockerEnv 返回真实容器里某个环境变量的值（连同 ok 表示这个键存不存在）
// ——用 `env` 列出全部再逐行匹配，而不是 `printenv KEY`（后者对不存在的
// 键和空字符串的键返回的退出码/输出没有区别，分不清"没有这个键"与
// "这个键是空串"，而用例 5 恰恰要断言的是前者）。
func dockerEnv(t *testing.T, container, key string) (string, bool) {
	t.Helper()
	out, err := exec.Command("docker", "exec", container, "env").CombinedOutput()
	if err != nil {
		t.Skipf("docker exec %s env 失败（容器不在跑？先 brickkit up）：%v\n%s", container, err, out)
	}
	prefix := key + "="
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix), true
		}
	}
	return "", false
}

// ────────────────────────────────────────────────────────────────
// 用例 3：地址变量名由组件 ID 推导、不带版本号（值带版本号）
// ────────────────────────────────────────────────────────────────

func TestPlatform03_地址变量名由ID推导不带版本号(t *testing.T) {
	requireCommand(t, "docker")
	val, ok := dockerEnv(t, erpSalesContainer, "MDM_CUSTOMER_ENDPOINT")
	if !ok {
		t.Fatal("期望 erp-sales 容器里有 MDM_CUSTOMER_ENDPOINT，实际没有这个键")
	}
	if val != "http://mdm-customer-1-0-0:8080" {
		t.Fatalf("期望 MDM_CUSTOMER_ENDPOINT=http://mdm-customer-1-0-0:8080，实际 %q", val)
	}
}

// ────────────────────────────────────────────────────────────────
// 用例 4：额外端口地址是 http:// 而不是 grpc://，且真的连得上
// ────────────────────────────────────────────────────────────────

func TestPlatform04_额外端口地址是http不是grpc(t *testing.T) {
	requireCommand(t, "docker")
	val, ok := dockerEnv(t, erpSalesContainer, "MDM_CUSTOMER_GRPC_ENDPOINT")
	if !ok {
		t.Fatal("期望 erp-sales 容器里有 MDM_CUSTOMER_GRPC_ENDPOINT，实际没有这个键")
	}
	if !strings.HasPrefix(val, "http://") {
		t.Fatalf("期望 MDM_CUSTOMER_GRPC_ENDPOINT 以 http:// 开头，实际 %q", val)
	}
	if strings.HasPrefix(val, "grpc://") {
		t.Fatalf("不该是 grpc:// 开头：%q", val)
	}
	// "剥掉 scheme 后真的能连上"这一半在 Task 17/18 的真故障注入测试与
	// 端到端 CreateOrder→ConfirmOrder→ShipOrder 全链路里已经反复验证过
	// （erp-sales 的 backend/internal/client 正是用 besdk.Endpoint() 剥
	// 掉这个 http:// 前缀后再拨号，四条依赖边全部真实 gRPC 调用成功）。
}

// ────────────────────────────────────────────────────────────────
// 用例 5：弱依赖缺失时容器里根本没有这个键（不是空串）
// ────────────────────────────────────────────────────────────────

func TestPlatform05_弱依赖缺失时键根本不存在(t *testing.T) {
	requireCommand(t, "docker")
	val, ok := dockerEnv(t, erpSalesContainer, "INFRA_WORKFLOW_ENDPOINT")
	if ok {
		t.Fatalf("infra/workflow 是缺失的弱依赖，期望 INFRA_WORKFLOW_ENDPOINT 这个键根本不存在，实际存在且值为 %q", val)
	}
}

// ────────────────────────────────────────────────────────────────
// 用例 8：启停跟着上层走（enabled: false 级联）；「删条目」与
// 「enabled: false」不等价（§9.5）——通用机制，用隔离 fixture 验，
// 不扰动真实跑着的五组件部署。
// ────────────────────────────────────────────────────────────────

func TestPlatform08a_顶层enabled为false下层跟着不启动(t *testing.T) {
	f := newFixture(t)
	f.writeFile("brickkit.yaml", `project: b8a
deploy:
  target: docker
sources:
  - id: local-dev
    type: local
    path: ./components
components:
  - id: foo/bar
    version: 1.0.0
    enabled: false
  - id: baz/qux
    version: 1.0.0
`)
	f.writeFile("components/foo/bar/component.yaml", minimalComponent("foo/bar", "1.0.0", 19001))
	f.writeFile("components/baz/qux/component.yaml", `apiVersion: brickkit/v1
kind: Component
metadata:
  id: baz/qux
  name: 依赖方
  version: 1.0.0
  description: 强依赖 foo/bar
  license: Apache-2.0
  vendor: brickKit
dependencies:
  components:
    - foo/bar@1.0.0
deployment:
  type: container
  image: brickenterprise/fixture:1.0.0
  port: 19002
healthCheck:
  type: http
  path: /healthz
`)
	res := f.run("up", "--dry-run")
	// foo/bar 顶层 enabled: false，baz/qux 强依赖它——按"启停跟着上层走"
	// （总纲/导读四条铁律之一）实际是**优雅级联**，不是硬报错：exitCode
	// 仍是 0，两个组件都被标成"不启动"，理由分别是"显式禁用"和"强依赖
	// 不启动"。第一次写这条测试时想当然地假设会报错阻断，实测才发现
	// 是这个更温和的行为——`brickkit up` 的输出会明确列出"本次没有组件
	// 会启动"，而不是把这当成一个配置错误。
	if res.exitCode != 0 {
		t.Fatalf("enabled:false 级联到强依赖方，是优雅降级不是报错，期望 exitCode=0，实际 %d：%s",
			res.exitCode, res.stdout)
	}
	if !strings.Contains(res.stdout, "foo/bar") || !strings.Contains(res.stdout, "显式禁用") {
		t.Fatalf("期望输出点出 foo/bar 是显式禁用，实际：%s", res.stdout)
	}
	if !strings.Contains(res.stdout, "baz/qux") {
		t.Fatalf("期望输出提到 baz/qux 因为强依赖不启动，实际：%s", res.stdout)
	}
	if strings.Contains(res.stdout, "FOO_BAR_ENDPOINT") {
		t.Fatalf("foo/bar 被禁用，不该有任何 FOO_BAR_ENDPOINT 被注入：%s", res.stdout)
	}
}

func TestPlatform08b_删条目与enabled为false不等价(t *testing.T) {
	f := newFixture(t)
	// ⚠️ 刻意用跟 08a **完全相同**的 foo/bar→baz/qux 强依赖结构，只改
	// 一处：foo/bar 顶层条目整条不写（不是写 enabled: false）——这样
	// 两条测试才是真正意义上的对照组，差异只在这一个变量上。
	//
	// 导读第 10 条的原话："客户没买的组件写 enabled: false——它下面那
	// 一串跟着级联不启动……没买的正确写法是整条不写进 brickkit.yaml"。
	// 第一次写这条测试时想当然地假设"不写条目"会让 baz/qux 的强依赖
	// 落空（同 08a），实测才发现完全不是——只要 foo/bar 在安装源里能
	// 找到，"没有顶层条目"根本不影响强依赖的正常解析：brickkit 照样
	// 会把它自动拉进来启动。这才是"不等价"的真正含义：`enabled: false`
	// 是**显式**声明"我知道这个组件存在，但明确不要它启动"，会强制
	// 级联断开所有需要它的边；不写条目只是**没有主动要求**它启动，
	// 依赖解析该怎么满足还是怎么满足——这两者只有在"真的没人需要它"
	// 时才会得到相同的结果（都不启动），一旦有真实依赖边存在就会分道
	// 扬镳，08a/08b 这一组对照正是在验证后一种情形。
	f.writeFile("brickkit.yaml", `project: b8b
deploy:
  target: docker
sources:
  - id: local-dev
    type: local
    path: ./components
components:
  - id: baz/qux
    version: 1.0.0
`)
	f.writeFile("components/foo/bar/component.yaml", minimalComponent("foo/bar", "1.0.0", 19001))
	f.writeFile("components/baz/qux/component.yaml", `apiVersion: brickkit/v1
kind: Component
metadata:
  id: baz/qux
  name: 依赖方
  version: 1.0.0
  description: 强依赖 foo/bar
  license: Apache-2.0
  vendor: brickKit
dependencies:
  components:
    - foo/bar@1.0.0
deployment:
  type: container
  image: brickenterprise/fixture:1.0.0
  port: 19002
healthCheck:
  type: http
  path: /healthz
`)
	res := f.run("up", "--dry-run")
	if res.exitCode != 0 {
		t.Fatalf("删条目（不是 enabled: false）不该阻断——foo/bar 在安装源里找得到，强依赖正常解析拉进来即可，实际失败：%s", res.stdout)
	}
	if !strings.Contains(res.stdout, "foo/bar") || !strings.Contains(res.stdout, "baz/qux 需要") {
		t.Fatalf("期望 foo/bar 因为 baz/qux 的强依赖被自动拉进来启动，实际输出：%s", res.stdout)
	}
	if !strings.Contains(f.generatedCompose(), "FOO_BAR_ENDPOINT") {
		t.Fatalf("期望 baz/qux 容器里真的注入了 FOO_BAR_ENDPOINT（对照 08a：那边是彻底没有），实际 compose：%s", f.generatedCompose())
	}
}
