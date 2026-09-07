// Group A · 阶段一已人工验过、本阶段固化成可重跑测试的断言（用例
// 1、2、10、17）。
package platform_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// ────────────────────────────────────────────────────────────────
// 用例 1：未知键当场报错，不是静默忽略
// ────────────────────────────────────────────────────────────────

func TestPlatform01_未知键当场报错(t *testing.T) {
	f := newFixture(t)
	f.writeFile("brickkit.yaml", `project: a1
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
	// assembly_role 是 assembly.yaml 的字段，不是 component.yaml 的——
	// 平台断言 1 验的就是这种混放会被当场拒绝，不是静默忽略（决策 84）。
	f.writeFile("components/foo/bar/component.yaml", `apiVersion: brickkit/v1
kind: Component
metadata:
  id: foo/bar
  name: 断言测试假组件
  version: 1.0.0
  description: 平台断言测试用
  license: Apache-2.0
  vendor: brickKit
  assembly_role: optional
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
		t.Fatalf("期望未知键 assembly_role 当场报错，实际成功了：%s", res.stdout)
	}
	if !strings.Contains(res.stdout, "assembly_role") {
		t.Fatalf("期望错误信息点出未知键 assembly_role，实际输出：%s", res.stdout)
	}
}

// ────────────────────────────────────────────────────────────────
// 用例 2：assembly.yaml 靠 artifacts 随组件分发
// ────────────────────────────────────────────────────────────────

func TestPlatform02_assemblyYaml靠artifacts分发(t *testing.T) {
	f := newFixture(t)
	f.writeFile("brickkit.yaml", `project: a2
deploy:
  target: docker
sources:
  - id: local-dev
    type: local
    path: ./components
components: []
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
artifacts:
  - type: metadata
    files: [assembly.yaml]
deployment:
  type: container
  image: brickenterprise/fixture:1.0.0
  port: 19001
healthCheck:
  type: http
  path: /healthz
`)
	f.writeFile("components/foo/bar/assembly.yaml", "id: foo/bar\nversion: 1.0.0\n")

	// ⚠️ 产物分发发生在 `brickkit add`（004 §3.3 步骤 6："下载 artifacts
	// 到 .brickkit/artifacts/<版本化服务名>/"），不是 `up`——实测验证过：
	// `up`/`up --dry-run` 跑完 .brickkit 下只有 generated/ 与 manifests/
	// 两个目录，从不下载/分发任何 artifacts，即使容器真的变 healthy 也
	// 一样。第一次写这条测试时想当然地用了 `up`，读文件报"不存在"才顺藤
	// 摸瓜查到 brickKit 源码（internal/cli/artifacts.go 的
	// downloadArtifacts 只被 add.go/add_local.go 调用）确认的。
	res := f.run("add", "foo/bar@1.0.0", "--yes")
	if res.exitCode != 0 {
		t.Fatalf("期望成功，实际退出码 %d：%s", res.exitCode, res.stdout)
	}
	data, err := os.ReadFile(f.dir + "/.brickkit/artifacts/foo-bar-1-0-0/metadata/assembly.yaml")
	if err != nil {
		t.Fatalf("读分发出来的 assembly.yaml 失败：%v", err)
	}
	got := string(data)
	if !strings.Contains(got, "id: foo/bar") {
		t.Fatalf("期望分发出来的 assembly.yaml 内容里有 id: foo/bar，实际：%s", got)
	}
}

// ────────────────────────────────────────────────────────────────
// 用例 10：默认启动宽限是 60 秒——不只是验配置值，真的造一个 45 秒
// 冷启动的组件验证它顶得住
// ────────────────────────────────────────────────────────────────

// TestPlatform10a_配置值确实是60秒 验的是配置本身，阶段一已经用真机
// docker inspect 验过一次（对着 mdm-customer），这里换成假组件复现同一
// 件事，不依赖业务组件是否还在。
func TestPlatform10a_配置值确实是60秒(t *testing.T) {
	docker := requireCommand(t, "docker")
	f := newFixture(t)
	f.writeFile("brickkit.yaml", `project: a10a
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
	f.writeFile("components/foo/bar/component.yaml", minimalComponent("foo/bar", "1.0.0", 19001))
	res := f.run("up", "--dry-run")
	if res.exitCode != 0 {
		t.Fatalf("期望成功，实际退出码 %d：%s", res.exitCode, res.stdout)
	}
	compose := f.generatedCompose()
	if !strings.Contains(compose, "start_period: 60s") {
		t.Fatalf("期望生成的 compose 里健康检查 start_period 是 60s，实际：%s", compose)
	}
	_ = docker // 本条只读生成文件，不需要真的起容器
}

const (
	slowStartImage     = "brickenterprise/fixture-slow-start:1.0.0"
	slowStartComponent = "fixture/slow-start"
)

// TestPlatform10b_真实45秒冷启动组件不会被误杀 是本条断言阶段一遗留
// 的缺口：只验了配置值是 60 秒，没有验证「一个真的接近该阈值的冷启动
// 组件真能撑住」。fixtures/slow-start 是特意写的假组件（sleep 45 秒
// 才开始响应 /healthz），需要先 `make -C fixtures/slow-start build`
// 建出镜像；镜像不在本地就跳过，不是放宽断言。
func TestPlatform10b_真实45秒冷启动组件不会被误杀(t *testing.T) {
	requireCommand(t, "docker")
	if out, err := exec.Command("docker", "image", "inspect", slowStartImage).CombinedOutput(); err != nil {
		t.Skipf("镜像 %s 不在本地（先 make -C fixtures/slow-start build）：%v\n%s", slowStartImage, err, out)
	}

	f := newFixture(t)
	f.writeFile("brickkit.yaml", `project: a10b
deploy:
  target: docker
sources:
  - id: local-dev
    type: local
    path: ./components
components:
  - id: `+slowStartComponent+`
    version: 1.0.0
`)
	f.writeFile("components/fixture/slow-start/component.yaml", readFixtureComponentYAML(t))

	start := time.Now()
	res := f.run("up")
	elapsed := time.Since(start)
	t.Cleanup(func() { f.run("down") })

	if res.exitCode != 0 {
		t.Fatalf("45 秒冷启动组件应该被 60 秒宽限顶住、最终成功，实际失败（耗时 %s）：%s", elapsed, res.stdout)
	}
	if !strings.Contains(res.stdout, "healthy") {
		t.Fatalf("期望输出里组件状态是 healthy，实际：%s", res.stdout)
	}
	if elapsed < 40*time.Second {
		t.Fatalf("期望 up 真的等了将近 45 秒冷启动（不是被 mock 掉的），实际只花了 %s", elapsed)
	}

	container := containerName("a10b", "fixture-slow-start-1-0-0")
	out, err := exec.Command("docker", "inspect", "-f", "{{.RestartCount}}", container).CombinedOutput()
	if err != nil {
		t.Fatalf("docker inspect 失败：%v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "0" {
		t.Fatalf("45 秒冷启动期间容器不该被重启过，实际 RestartCount=%s", strings.TrimSpace(string(out)))
	}
}

// ────────────────────────────────────────────────────────────────
// 用例 17：up 只打印建库语句，不真的建库
// ────────────────────────────────────────────────────────────────

func TestPlatform17_up只打印建库语句不真的建库(t *testing.T) {
	f := newFixture(t)
	f.writeFile("brickkit.yaml", `project: a17
deploy:
  target: docker
sources:
  - id: local-dev
    type: local
    path: ./components
components:
  - id: foo/bar
    version: 1.0.0
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
        database: a17_never_created_db
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
dependencies:
  resources:
    - { kind: database, engine: postgresql }
deployment:
  type: container
  image: brickenterprise/fixture:1.0.0
  port: 19001
healthCheck:
  type: http
  path: /healthz
`)
	res := f.run("up", "--dry-run")
	if !strings.Contains(res.stdout, "CREATE DATABASE") {
		t.Fatalf("期望输出里出现 CREATE DATABASE 语句，实际：%s", res.stdout)
	}
	if !strings.Contains(res.stdout, "a17_never_created_db") {
		t.Fatalf("期望建库语句点名 a17_never_created_db，实际：%s", res.stdout)
	}
	// --dry-run 不连数据库，这里没有第二步"真的去查库里有没有这张库"——
	// 分开验证过：--dry-run 全程不需要数据库可达（fixture 用的
	// host.docker.internal 在这个隔离环境里本来就连不上，命令依然成功，
	// 从行为上间接证明它没有真的去执行这条 SQL）。
}

// readFixtureComponentYAML 读 fixtures/slow-start/component.yaml 原文
// ——避免和真正用来建镜像的那份文件出现两份不同步的拷贝。
func readFixtureComponentYAML(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("fixtures/slow-start/component.yaml")
	if err != nil {
		t.Fatalf("读 fixtures/slow-start/component.yaml 失败：%v", err)
	}
	return string(data)
}

// containerName 拼出 brickkit 生成的容器名（<project>-<服务名>-1 的
// docker compose 默认命名规则，带 brickkit-<project> 前缀）。project
// 由调用方传入——每条测试自己在 brickkit.yaml 里写死了 project 名，
// 不需要反过来解析文件。
func containerName(project, serviceName string) string {
	return "brickkit-" + project + "-" + serviceName + "-1"
}
