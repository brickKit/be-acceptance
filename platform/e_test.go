// Group E · 纯配置/边界断言（用例 6、7、9、19、20）——不需要真组件，
// 临时 manifest + --dry-run 就能验完。
package platform_test

import (
	"strings"
	"testing"
)

// ────────────────────────────────────────────────────────────────
// 用例 6：保留后缀 *_ENDPOINT 会被跳过，只给警告
// ────────────────────────────────────────────────────────────────

func TestPlatform06_保留变量后缀被跳过且只警告(t *testing.T) {
	f := newFixture(t)
	f.writeFile("brickkit.yaml", `project: e6
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
  name: 断言测试假组件
  version: 1.0.0
  description: 平台断言测试用
  license: Apache-2.0
  vendor: brickKit
configSchema:
  type: object
  properties:
    otelEndpoint:
      type: string
      default: "http://should-not-appear:4318"
deployment:
  type: container
  image: brickenterprise/fixture:1.0.0
  port: 19001
healthCheck:
  type: http
  path: /healthz
`)
	res := f.run("up", "--dry-run")
	if res.exitCode != 0 {
		t.Fatalf("命中保留后缀应该只警告不阻断，实际退出码 %d：%s", res.exitCode, res.stdout)
	}
	if !strings.Contains(res.stdout, "OTEL_ENDPOINT") || !strings.Contains(res.stdout, "已被忽略") {
		t.Fatalf("期望警告里点名 OTEL_ENDPOINT 且说明已被忽略，实际输出：%s", res.stdout)
	}
	compose := f.generatedCompose()
	if strings.Contains(compose, "OTEL_ENDPOINT") {
		t.Fatalf("命中保留后缀的配置项不该真的注入进容器环境，实际 compose 里出现了 OTEL_ENDPOINT：%s", compose)
	}
}

// ────────────────────────────────────────────────────────────────
// 用例 7：config 数组会被渲染成 "[a b c]"（空格分隔），不能当逗号
// 分隔字符串直接用
// ────────────────────────────────────────────────────────────────

func TestPlatform07_config数组渲染成方括号空格分隔(t *testing.T) {
	f := newFixture(t)
	f.writeFile("brickkit.yaml", `project: e7
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
  name: 断言测试假组件
  version: 1.0.0
  description: 平台断言测试用
  license: Apache-2.0
  vendor: brickKit
configSchema:
  type: object
  properties:
    enabledFeatures:
      type: array
      default: [a, b, c]
deployment:
  type: container
  image: brickenterprise/fixture:1.0.0
  port: 19001
healthCheck:
  type: http
  path: /healthz
`)
	res := f.run("up", "--dry-run")
	if res.exitCode != 0 {
		t.Fatalf("期望 up --dry-run 成功，实际退出码 %d：%s", res.exitCode, res.stdout)
	}
	compose := f.generatedCompose()
	if !strings.Contains(compose, "ENABLED_FEATURES=[a b c]") {
		t.Fatalf("期望数组配置渲染成 ENABLED_FEATURES=[a b c]（空格分隔，不可用），实际 compose：%s", compose)
	}
}

// ────────────────────────────────────────────────────────────────
// 用例 9：Fork 遮蔽按 sources 顺序（靠前的赢）；metadata.id 改名后
// 依赖方拿到的 *_ENDPOINT 消失
// ────────────────────────────────────────────────────────────────

func TestPlatform09a_同id同version两份源靠前的赢(t *testing.T) {
	f := newFixture(t)
	f.writeFile("brickkit.yaml", `project: e9a
deploy:
  target: docker
sources:
  - id: source-a
    type: local
    path: ./sourceA
  - id: source-b
    type: local
    path: ./sourceB
components:
  - id: foo/bar
    version: 1.0.0
`)
	f.writeFile("sourceA/foo/bar/component.yaml", `apiVersion: brickkit/v1
kind: Component
metadata:
  id: foo/bar
  name: 来自A源
  version: 1.0.0
  description: MARKER-SOURCE-A
  license: Apache-2.0
  vendor: brickKit
deployment:
  type: container
  image: brickenterprise/fixture:1.0.0
  port: 19001
healthCheck:
  type: http
  path: /healthz
`)
	f.writeFile("sourceB/foo/bar/component.yaml", `apiVersion: brickkit/v1
kind: Component
metadata:
  id: foo/bar
  name: 来自B源
  version: 1.0.0
  description: MARKER-SOURCE-B
  license: Apache-2.0
  vendor: brickKit
deployment:
  type: container
  image: brickenterprise/fixture:1.0.0
  port: 19001
healthCheck:
  type: http
  path: /healthz
`)
	res := f.run("up", "--dry-run")
	if res.exitCode != 0 {
		t.Fatalf("期望成功，实际退出码 %d：%s", res.exitCode, res.stdout)
	}
	cached := f.manifestCache("foo-bar", "1.0.0")
	if !strings.Contains(cached, "MARKER-SOURCE-A") {
		t.Fatalf("靠前的源（source-a）应该赢，实际缓存的 manifest：%s", cached)
	}
	if strings.Contains(cached, "MARKER-SOURCE-B") {
		t.Fatalf("靠后的源（source-b）不该被用到，实际缓存的 manifest 里却出现了它的标记：%s", cached)
	}
}

func TestPlatform09b_改metadataId后依赖方的ENDPOINT消失(t *testing.T) {
	f := newFixture(t)
	// ⚠️ 只用一个源，避免第二个源里同 id 的副本"顶上来"掩盖真实结果
	// （本条曾经因为复用了 09a 的双源 fixture、误判过一次假阳性——
	// 断言 9 的两半必须用互相隔离的 fixture，见 docs/dev/实测踩坑记录.md）。
	f.writeFile("brickkit.yaml", `project: e9b
deploy:
  target: docker
sources:
  - id: source-a
    type: local
    path: ./sourceA
components:
  - id: baz/qux
    version: 1.0.0
`)
	f.writeFile("sourceA/foo/bar/component.yaml", `apiVersion: brickkit/v1
kind: Component
metadata:
  id: foo/bar
  name: 依赖目标
  version: 1.0.0
  description: 待改名的组件
  license: Apache-2.0
  vendor: brickKit
deployment:
  type: container
  image: brickenterprise/fixture:1.0.0
  port: 19001
healthCheck:
  type: http
  path: /healthz
`)
	f.writeFile("sourceA/baz/qux/component.yaml", `apiVersion: brickkit/v1
kind: Component
metadata:
  id: baz/qux
  name: 依赖方
  version: 1.0.0
  description: 依赖 foo/bar 的组件
  license: Apache-2.0
  vendor: brickKit
dependencies:
  components:
    - { id: foo/bar@1.0.0, optional: true }
deployment:
  type: container
  image: brickenterprise/fixture:1.0.0
  port: 19002
healthCheck:
  type: http
  path: /healthz
`)
	// 改名前：ENDPOINT 应该存在。
	res := f.run("up", "--dry-run")
	if res.exitCode != 0 {
		t.Fatalf("改名前应该成功，实际退出码 %d：%s", res.exitCode, res.stdout)
	}
	if !strings.Contains(f.generatedCompose(), "FOO_BAR_ENDPOINT") {
		t.Fatalf("改名前 baz/qux 容器里应该有 FOO_BAR_ENDPOINT，实际 compose：%s", f.generatedCompose())
	}

	// 把 foo/bar 的 metadata.id 改成 foo/bar2（目录本身不动）。
	f.writeFile("sourceA/foo/bar/component.yaml", `apiVersion: brickkit/v1
kind: Component
metadata:
  id: foo/bar2
  name: 依赖目标
  version: 1.0.0
  description: 待改名的组件
  license: Apache-2.0
  vendor: brickKit
deployment:
  type: container
  image: brickenterprise/fixture:1.0.0
  port: 19001
healthCheck:
  type: http
  path: /healthz
`)
	res = f.run("up", "--dry-run")
	if res.exitCode != 0 {
		t.Fatalf("弱依赖找不到只该警告，不该阻断，实际退出码 %d：%s", res.exitCode, res.stdout)
	}
	if !strings.Contains(res.stdout, "弱依赖缺失") || !strings.Contains(res.stdout, "FOO_BAR_ENDPOINT") {
		t.Fatalf("期望警告说清「弱依赖缺失」且点名 FOO_BAR_ENDPOINT 不会被注入，实际输出：%s", res.stdout)
	}
	if strings.Contains(f.generatedCompose(), "FOO_BAR_ENDPOINT") {
		t.Fatalf("metadata.id 改名后，依赖方容器里不该再有 FOO_BAR_ENDPOINT，实际 compose：%s", f.generatedCompose())
	}
}

// ────────────────────────────────────────────────────────────────
// 用例 19：版本必须精确，^1.2 这类范围约束直接报错
// ────────────────────────────────────────────────────────────────

func TestPlatform19_版本范围约束直接报错(t *testing.T) {
	f := newFixture(t)
	f.writeFile("brickkit.yaml", `project: e19
deploy:
  target: docker
sources:
  - id: local-dev
    type: local
    path: ./components
components:
  - id: foo/bar
    version: ^1.2
`)
	f.writeFile("components/foo/bar/component.yaml", minimalComponent("foo/bar", "1.0.0", 19001))
	res := f.run("up", "--dry-run")
	if res.exitCode == 0 {
		t.Fatalf("期望 ^1.2 这种范围版本被拒绝，实际成功了：%s", res.stdout)
	}
	if !strings.Contains(res.stdout, "精确版本") {
		t.Fatalf("期望错误信息点出「精确版本」的要求，实际输出：%s", res.stdout)
	}
}

// ────────────────────────────────────────────────────────────────
// 用例 20：requireSignature: true 时本地源不受签名约束，照装
// ────────────────────────────────────────────────────────────────

func TestPlatform20_本地源不受签名约束(t *testing.T) {
	f := newFixture(t)
	f.writeFile("brickkit.yaml", `project: e20
deploy:
  target: docker
installer:
  requireSignature: true
sources:
  - id: local-dev
    type: local
    path: ./components
components:
  - id: foo/bar
    version: 1.0.0
`)
	f.writeFile("components/foo/bar/component.yaml", minimalComponent("foo/bar", "1.0.0", 19001))
	res := f.run("up", "--dry-run")
	if res.exitCode != 0 {
		t.Fatalf("requireSignature: true 不该拦住本地源组件（§8.5.2 只有市场安装源受签名约束），实际退出码 %d：%s",
			res.exitCode, res.stdout)
	}
}
