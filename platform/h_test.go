// Group H · brickKit 请求真机验证"servedBy 混合部署场景"时顺带补上的两条
// 通用平台断言（用例 26-27）——跟 servedBy 本身无关，是 brickKit 已有的
// "精确版本 + 版本化服务名"机制此前从未被显式测试过的两个角落，验证过程
// 见 docs/dev/给brickKit的反馈.md 相关章节。
package platform_test

import (
	"strings"
	"testing"
)

// ────────────────────────────────────────────────────────────────
// 用例 26：同一个组件 ID 的两个精确版本可以在同一份 brickkit.yaml 里
// 同时声明为独立部署的顶层组件，各自的依赖方拿到正确指向"自己那个版本"
// 的地址，互不干扰
// ────────────────────────────────────────────────────────────────
//
// brickKit 在准备开发 servedBy 前，推演"版本迁移期间一部分调用方还没
// 升级"这类场景时，想确认这条更基础的机制本身是不是真的成立——这条测试
// 完全不涉及 servedBy，只验证"精确版本 + 版本化服务名"这个平台已有能力。
//
// ⚠️ 一次没有命中的尝试：最初把两个版本的 component.yaml 都放在同一个
// local 源目录（`./components/foo/bar/component.yaml`）里，只是内容
// 分别写 1.0.0/2.0.0 覆盖着写——结果 `brickkit up` 直接报
// COMPONENT_NOT_FOUND（"安装源里有这个组件，但版本不是要的那个"）。
// 根因：一个 local 源的一个组件目录物理上只能同时存在一份 component.yaml，
// 天然只服务一个版本；`.brickkit/manifests/` 缓存能让"改名后旧版本仍可从
// 缓存服务"这种场景成立（platform/README.md 用例 9 后半段记录过的机制），
// 但一次性同时要两个版本，缓存里没有旧版本的记录，一样拿不到。真正的
// 正确做法是两个独立的源（各自的目录物理上只放一个版本），源解析按
// "声明顺序依次尝试，谁真的有这个精确版本谁提供"（既有判据）自然给出
// 正确结果，不需要缓存介入。
func TestPlatform26_同一个组件ID两个精确版本同时声明地址互不干扰(t *testing.T) {
	f := newFixture(t)
	f.writeFile("brickkit.yaml", `project: h26
deploy:
  target: docker
sources:
  - id: source-v1
    type: local
    path: ./sourceV1
  - id: source-v2
    type: local
    path: ./sourceV2
components:
  - id: foo/bar
    version: 1.0.0
  - id: foo/bar
    version: 2.0.0
  - id: legacy/caller
    version: 1.0.0
  - id: new/caller
    version: 1.0.0
`)
	f.writeFile("sourceV1/foo/bar/component.yaml", `apiVersion: brickkit/v1
kind: Component
metadata:
  id: foo/bar
  name: 目标组件（旧版本）
  version: 1.0.0
  description: 平台断言测试用
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
	f.writeFile("sourceV2/foo/bar/component.yaml", `apiVersion: brickkit/v1
kind: Component
metadata:
  id: foo/bar
  name: 目标组件（新版本）
  version: 2.0.0
  description: 平台断言测试用
  license: Apache-2.0
  vendor: brickKit
deployment:
  type: container
  image: brickenterprise/fixture:1.0.0
  port: 19004
healthCheck:
  type: http
  path: /healthz
`)
	f.writeFile("sourceV1/legacy/caller/component.yaml", `apiVersion: brickkit/v1
kind: Component
metadata:
  id: legacy/caller
  name: 旧调用方
  version: 1.0.0
  description: 依赖旧版本
  license: Apache-2.0
  vendor: brickKit
dependencies:
  components:
    - { id: foo/bar@1.0.0, optional: false }
deployment:
  type: container
  image: brickenterprise/fixture:1.0.0
  port: 19002
healthCheck:
  type: http
  path: /healthz
`)
	f.writeFile("sourceV1/new/caller/component.yaml", `apiVersion: brickkit/v1
kind: Component
metadata:
  id: new/caller
  name: 新调用方
  version: 1.0.0
  description: 依赖新版本
  license: Apache-2.0
  vendor: brickKit
dependencies:
  components:
    - { id: foo/bar@2.0.0, optional: false }
deployment:
  type: container
  image: brickenterprise/fixture:1.0.0
  port: 19003
healthCheck:
  type: http
  path: /healthz
`)
	res := f.run("up", "--dry-run")
	if res.exitCode != 0 {
		t.Fatalf("期望成功，实际退出码 %d：%s", res.exitCode, res.stdout)
	}
	compose := f.generatedCompose()
	if !strings.Contains(compose, "foo-bar-1-0-0:") {
		t.Fatalf("期望生成 foo-bar-1-0-0 这个独立 service，实际 compose：%s", compose)
	}
	if !strings.Contains(compose, "foo-bar-2-0-0:") {
		t.Fatalf("期望生成 foo-bar-2-0-0 这个独立 service，实际 compose：%s", compose)
	}
	if !strings.Contains(compose, "FOO_BAR_ENDPOINT=http://foo-bar-1-0-0:19001") {
		t.Fatalf("期望 legacy/caller 拿到指向旧版本的 FOO_BAR_ENDPOINT，实际 compose：%s", compose)
	}
	if !strings.Contains(compose, "FOO_BAR_ENDPOINT=http://foo-bar-2-0-0:19004") {
		t.Fatalf("期望 new/caller 拿到指向新版本的 FOO_BAR_ENDPOINT，实际 compose：%s", compose)
	}
}

// ────────────────────────────────────────────────────────────────
// 用例 27：同一个组件 ID 的同一个精确版本重复声明两次，直接拒绝
// ────────────────────────────────────────────────────────────────
//
// brickKit 自己在 servedBy 请求文档里预判"这个场景做不到，而且不应该
// 做到"（同一个 ID+精确版本只能出现一次，是既有校验，不是 servedBy
// 新加的规则）——这条测试是对这个预判的真机确认，不是新发现。
func TestPlatform27_同id同精确版本重复声明直接拒绝(t *testing.T) {
	f := newFixture(t)
	f.writeFile("brickkit.yaml", `project: h27
deploy:
  target: docker
sources:
  - id: local-dev
    type: local
    path: ./components
components:
  - id: foo/bar
    version: 1.0.0
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
deployment:
  type: container
  image: brickenterprise/fixture:1.0.0
  port: 19001
healthCheck:
  type: http
  path: /healthz
`)
	res := f.run("up", "--dry-run")
	if res.exitCode == 0 {
		t.Fatalf("期望同 ID 同精确版本重复声明直接报错，实际成功了：%s", res.stdout)
	}
	if !strings.Contains(res.stdout, "重复声明") {
		t.Fatalf("期望错误信息点出重复声明，实际输出：%s", res.stdout)
	}
}
