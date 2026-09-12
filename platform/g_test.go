// Group G · 阶段三 Task 15 新增的平台断言（用例 21-22）。
//
// 阶段三计划原文列了 4 条候选（Python 冷启动宽限、TS 组件基本行为、
// DLQ 往返、GraphQL 深度/复杂度限制），阶段三主线收尾时被跳过、一直
// 拖到这次系统排查才补上。逐条核对之后：
//
//   - DLQ 往返：be-sdk-go 自己的 events_test.go 里
//     TestConsume_hop_count超过5丢弃进DLQ 已经是一条常驻的、可重跑的
//     go test，测的正是这条计划要的"SDK 层的行为本身"——不用在这里
//     重写一份重复的，那只会变成"两处维护同一件事，其中一处迟早不同步"。
//   - GraphQL 深度/复杂度限制：be-sdk-ts 自己的 graphqlServer.test.ts
//     里"白名单里 depth=6 的查询依然被拒绝"已经是一条常驻回归测试，
//     同样不用重复。
//
// 这两条已经在正确的地方（SDK 自己的仓库，测 SDK 自己的机制）落地了，
// 只是没有人回头在这里的验收清单上勾掉——本文件只补真正还没有任何
// 常驻测试覆盖的那两条：Python 组件的冷启动宽限、TS 组件是否重演了
// Python 那次真机踩过的"HEAD 请求 405"同类框架坑。
package platform_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ────────────────────────────────────────────────────────────────
// 用例 21：Python 组件的冷启动宽限（120 秒，不是 Go 的默认 60 秒）
// 真的生效
// ────────────────────────────────────────────────────────────────
//
// 分两半验证，同用例 10a/10b 的既有思路：①平台机制本身——一个显式
// 声明 startPeriodSeconds: 120 的组件，生成的 compose 里是不是真的写
// 着 120s，不管这个组件是不是"真的用 Python 写的"，平台只认这个字段
// 的值，不认语言（真机验证过：componentyaml.startPeriodSeconds 没有
// 任何"按语言自动推导默认值"的机制——每种语言的正确值是约定，得组件
// 作者自己写对，见总纲全局约束 §K3）；②真实约定是不是真的被遵守——
// 本仓库唯一的 Python 组件 infra-print 自己的 component.yaml 是不是
// 真的写了 120，不是嘴上说说。
func TestPlatform21_Python组件冷启动宽限120秒真的生效(t *testing.T) {
	f := newFixture(t)
	f.writeFile("brickkit.yaml", `project: g21
deploy:
  target: docker
sources:
  - id: local-dev
    type: local
    path: ./components
components:
  - id: foo/bar
    version: 1.0.0
`)
	f.writeFile("components/foo/bar/component.yaml", `apiVersion: brickkit/v1
kind: Component
metadata:
  id: foo/bar
  name: 断言测试假组件（模拟 Python 组件的冷启动宽限声明）
  version: 1.0.0
  description: 平台断言测试用，不是真实业务组件
  license: Apache-2.0
  vendor: brickKit
deployment:
  type: container
  image: brickenterprise/fixture:1.0.0
  port: 19001
healthCheck:
  type: http
  path: /healthz
  startPeriodSeconds: 120
`)
	res := f.run("up", "--dry-run")
	if res.exitCode != 0 {
		t.Fatalf("期望成功，实际退出码 %d：%s", res.exitCode, res.stdout)
	}
	compose := f.generatedCompose()
	if !strings.Contains(compose, "start_period: 120s") {
		t.Fatalf("期望生成的 compose 里健康检查 start_period 是 120s（Python 组件的约定值），实际：%s", compose)
	}

	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "components/infra/print/component.yaml"))
	if err != nil {
		t.Skipf("读不到 infra-print/component.yaml（不在装配仓库里跑？）：%v", err)
	}
	if !strings.Contains(string(data), "startPeriodSeconds: 120") {
		t.Fatalf("infra-print 是本仓库唯一的 Python 组件，期望它自己声明 startPeriodSeconds: 120，实际 component.yaml 里没有这一行")
	}
}

// ────────────────────────────────────────────────────────────────
// 用例 22：TS 组件的 /healthz 对 GET 和 HEAD 是否表现一致
// ────────────────────────────────────────────────────────────────
//
// 直接动机：be-sdk-python 真机验证时踩过一个真实框架坑（A9）——
// FastAPI 的 APIRoute 不像 Starlette 文档说的那样自动给 GET 路由注册
// HEAD，导致 HEAD /healthz 返回 405，容器被判 unhealthy。这条坑是
// "读了框架文档就下结论，没有真的跑一次代码"造成的——TS 这边（Yoga
// 起的 HTTP 服务器）有没有同一类问题，同样不能只读文档，得真机打一次
// 两种方法都试一遍。用真实端口（infra-bff-mobile 的主端口，见
// registry/ports.tsv 的 exposePort=28500）直接打 HTTP，不进容器。
func TestPlatform22_TS组件healthz的GET与HEAD都返回200(t *testing.T) {
	base := "http://localhost:28500"
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		req, err := http.NewRequest(method, base+"/healthz", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Skipf("infra-bff-mobile 没在跑（先 brickkit up）：%v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s /healthz 期望 200（be-sdk-python 之前真机踩过 HEAD 请求 405 的同类框架坑，"+
				"见 field-tested-pitfalls-log.md A9），实际 %d", method, resp.StatusCode)
		}
	}
}

// ────────────────────────────────────────────────────────────────
// 用例 23：TS 组件的 Config camelCase 转换在真实容器里真的生效
// ────────────────────────────────────────────────────────────────
//
// be-sdk-ts 自己的 config.test.ts 已经在单测层面验证过转换函数本身
// 是对的；这里补的是平台生成 + SDK 运行两层真的接得上——真实容器里
// 存在按约定转换出来的环境变量名，不是只在单元测试的假输入上正确。
func TestPlatform23_TS组件Config的camelCase转换在真实容器里生效(t *testing.T) {
	requireCommand(t, "docker")
	container := dockerContainerByPrefix(t, "brickkit-be-assembly-standard-infra-bff-mobile-")
	if _, ok := dockerEnv(t, container, "OTEL_BASE_URL"); !ok {
		t.Fatal("期望 infra-bff-mobile 容器里有 OTEL_BASE_URL（otelBaseUrl 转 SCREAMING_SNAKE_CASE），实际没有这个键")
	}
}
