// Group C · local: true 模式族（用例 11、12、13、14）——临时 fixture +
// --dry-run 就能验完，不需要真的在 IDE 里起一个进程。
package platform_test

import (
	"strings"
	"testing"
)

// ────────────────────────────────────────────────────────────────
// 用例 11：local: true 的三件事——不生成 service、依赖方有
// extra_hosts、注入端口换成 localPort
// ────────────────────────────────────────────────────────────────

func TestPlatform11_local组件不生成service且依赖方端口换成localPort(t *testing.T) {
	f := newFixture(t)
	f.writeFile("brickkit.yaml", `project: c11
deploy:
  target: docker
sources:
  - id: local-dev
    type: local
    path: ./components
components:
  - id: foo/bar
    version: 1.0.0
    local: true
    localPort: 18001
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
		t.Fatalf("期望成功，实际退出码 %d：%s", res.exitCode, res.stdout)
	}
	compose := f.generatedCompose()
	if strings.Contains(compose, "foo-bar-1-0-0:") && strings.Contains(compose, "services:") {
		// 粗略防呆：foo-bar-1-0-0 不该作为 service 名出现（它只该出现在
		// baz-qux 的 extra_hosts / 环境变量值里，不该有自己的 service 块）。
		servicesSection := compose[strings.Index(compose, "services:"):]
		if strings.Contains(servicesSection, "\n  foo-bar-1-0-0:\n") {
			t.Fatalf("local: true 的组件不该生成自己的 service 块，实际 compose：%s", compose)
		}
	}
	if !strings.Contains(compose, "extra_hosts:") || !strings.Contains(compose, "foo-bar-1-0-0:host-gateway") {
		t.Fatalf("期望依赖方容器有 extra_hosts: foo-bar-1-0-0:host-gateway，实际 compose：%s", compose)
	}
	if !strings.Contains(compose, "FOO_BAR_ENDPOINT=http://foo-bar-1-0-0:18001") {
		t.Fatalf("期望 FOO_BAR_ENDPOINT 用 localPort（18001）不是组件声明的 deployment.port（19001），实际 compose：%s", compose)
	}
}

// ────────────────────────────────────────────────────────────────
// 用例 12：local 组件的额外端口不被改写（依赖方拿到的仍是 Manifest
// 里声明的端口）；两个 local 组件撞同一额外端口时 up 报错
// ────────────────────────────────────────────────────────────────

func TestPlatform12a_local组件的额外端口不被localPort改写(t *testing.T) {
	f := newFixture(t)
	f.writeFile("brickkit.yaml", `project: c12a
deploy:
  target: docker
sources:
  - id: local-dev
    type: local
    path: ./components
components:
  - id: foo/bar
    version: 1.0.0
    local: true
    localPort: 18001
  - id: baz/qux
    version: 1.0.0
`)
	f.writeFile("components/foo/bar/component.yaml", `apiVersion: brickkit/v1
kind: Component
metadata:
  id: foo/bar
  name: 测试组件
  version: 1.0.0
  description: 平台断言测试用
  license: Apache-2.0
  vendor: brickKit
deployment:
  type: container
  image: brickenterprise/fixture:1.0.0
  port: 19001
  extraPorts:
    - { name: grpc, port: 19091 }
healthCheck:
  type: http
  path: /healthz
`)
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
		t.Fatalf("期望成功，实际退出码 %d：%s", res.exitCode, res.stdout)
	}
	compose := f.generatedCompose()
	// 主端口用 localPort（18001），额外端口（grpc）维持 Manifest 里声明
	// 的原始端口（19091）——两者互不影响，没有对应的"localExtraPort"
	// 概念：开发者在 IDE 里跑的那个进程本来就同时监听这两个端口。
	if !strings.Contains(compose, "FOO_BAR_ENDPOINT=http://foo-bar-1-0-0:18001") {
		t.Fatalf("期望主端口用 localPort=18001，实际 compose：%s", compose)
	}
	if !strings.Contains(compose, "FOO_BAR_GRPC_ENDPOINT=http://foo-bar-1-0-0:19091") {
		t.Fatalf("期望额外端口(grpc)维持 Manifest 声明的 19091，不被 localPort 影响，实际 compose：%s", compose)
	}
}

func TestPlatform12b_两个local组件撞同一额外端口报错(t *testing.T) {
	f := newFixture(t)
	f.writeFile("brickkit.yaml", `project: c12b
deploy:
  target: docker
sources:
  - id: local-dev
    type: local
    path: ./components
components:
  - id: foo/bar
    version: 1.0.0
    local: true
    localPort: 18001
  - id: foo/baz
    version: 1.0.0
    local: true
    localPort: 18002
`)
	f.writeFile("components/foo/bar/component.yaml", `apiVersion: brickkit/v1
kind: Component
metadata:
  id: foo/bar
  name: 测试组件 bar
  version: 1.0.0
  description: 平台断言测试用
  license: Apache-2.0
  vendor: brickKit
deployment:
  type: container
  image: brickenterprise/fixture:1.0.0
  port: 19001
  extraPorts:
    - { name: grpc, port: 19099 }
healthCheck:
  type: http
  path: /healthz
`)
	f.writeFile("components/foo/baz/component.yaml", `apiVersion: brickkit/v1
kind: Component
metadata:
  id: foo/baz
  name: 测试组件 baz
  version: 1.0.0
  description: 平台断言测试用
  license: Apache-2.0
  vendor: brickKit
deployment:
  type: container
  image: brickenterprise/fixture:1.0.0
  port: 19002
  extraPorts:
    - { name: grpc, port: 19099 }
healthCheck:
  type: http
  path: /healthz
`)
	res := f.run("up", "--dry-run")
	if res.exitCode == 0 {
		t.Fatalf("期望两个 local 组件撞同一额外端口时报错，实际成功了：%s", res.stdout)
	}
	if !strings.Contains(res.stdout, "19099") {
		t.Fatalf("期望错误信息点出冲突的端口 19099，实际输出：%s", res.stdout)
	}
}

// ────────────────────────────────────────────────────────────────
// 用例 13：local: true 不生成迁移容器，给出警告
// ────────────────────────────────────────────────────────────────

func TestPlatform13_local组件不生成迁移容器(t *testing.T) {
	f := newFixture(t)
	f.writeFile("brickkit.yaml", `project: c13
deploy:
  target: docker
sources:
  - id: local-dev
    type: local
    path: ./components
components:
  - id: foo/bar
    version: 1.0.0
    local: true
resources:
  - id: pg
    kind: database
    engine: postgresql
    host: host.docker.internal
    port: 5432
    username: postgres
    password: x
    bindings:
      - componentId: foo/bar
        database: c13_db
`)
	f.writeFile("components/foo/bar/component.yaml", `apiVersion: brickkit/v1
kind: Component
metadata:
  id: foo/bar
  name: 测试组件
  version: 1.0.0
  description: 平台断言测试用
  license: Apache-2.0
  vendor: brickKit
dependencies:
  resources:
    - { kind: database, engine: postgresql }
deployment:
  type: container
  image: brickenterprise/fixture:1.0.0
  port: 19001
migration:
  command: ["./migrate", "up"]
healthCheck:
  type: http
  path: /healthz
`)
	res := f.run("up", "--dry-run")
	if res.exitCode != 0 {
		t.Fatalf("期望成功（只是提示，不阻断），实际退出码 %d：%s", res.exitCode, res.stdout)
	}
	if !strings.Contains(res.stdout, "local 组件的数据库迁移不会自动执行") {
		t.Fatalf("期望提示「local 组件的数据库迁移不会自动执行」，实际输出：%s", res.stdout)
	}
	if strings.Contains(f.generatedCompose(), "migration") {
		t.Fatalf("local: true 的组件不该生成迁移容器，实际 compose 里出现了 migration：%s", f.generatedCompose())
	}
}

// ────────────────────────────────────────────────────────────────
// 用例 14：local: true + deploy.target: k8s 在生成阶段直接报错
// ────────────────────────────────────────────────────────────────

func TestPlatform14_local加k8s生成阶段直接报错(t *testing.T) {
	f := newFixture(t)
	f.writeFile("brickkit.yaml", `project: c14
deploy:
  target: k8s
sources:
  - id: local-dev
    type: local
    path: ./components
components:
  - id: foo/bar
    version: 1.0.0
    local: true
`)
	f.writeFile("components/foo/bar/component.yaml", minimalComponent("foo/bar", "1.0.0", 19001))
	res := f.run("up", "--dry-run")
	if res.exitCode == 0 {
		t.Fatalf("期望 local:true + k8s 在生成阶段报错，实际成功了：%s", res.stdout)
	}
	if !strings.Contains(res.stdout, "local") || !strings.Contains(res.stdout, "docker") {
		t.Fatalf("期望错误信息点出 local: true 只能配 docker，实际输出：%s", res.stdout)
	}
}
