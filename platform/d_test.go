// Group D · K8s 生成期断言（用例 15、16）——deploy.target: k8s + --dry-run
// 出清单，不需要真集群。
package platform_test

import (
	"strings"
	"testing"
)

// ────────────────────────────────────────────────────────────────
// 用例 15：K8s 下 component.yaml 的 deployment.labels 落在 Deployment
// 与 Pod 模板的 annotations 两处
// ────────────────────────────────────────────────────────────────

func TestPlatform15_K8s下labels落在Deployment与Pod两处(t *testing.T) {
	f := newFixture(t)
	f.writeFile("brickkit.yaml", `project: d15
deploy:
  target: k8s
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
deployment:
  type: container
  image: brickenterprise/fixture:1.0.0
  port: 19001
  labels:
    prometheus.io/scrape: "true"
    prometheus.io/port: "19001"
healthCheck:
  type: http
  path: /healthz
`)
	res := f.run("up", "--dry-run")
	if res.exitCode != 0 {
		t.Fatalf("期望成功，实际退出码 %d：%s", res.exitCode, res.stdout)
	}
	deploy := f.generatedFile("k8s/deployments/foo-bar-1-0-0.yaml")

	// Deployment 资源本身的 annotations（顶层 metadata）。
	topSection := deploy[:strings.Index(deploy, "spec:")]
	if !strings.Contains(topSection, "prometheus.io/scrape") {
		t.Fatalf("期望 Deployment 顶层 annotations 里有 prometheus.io/scrape，实际：\n%s", topSection)
	}

	// Pod 模板（spec.template.metadata）的 annotations——必须在 spec:
	// 之后再出现一次，同一份内容落在两个不同层级。
	restSection := deploy[strings.Index(deploy, "spec:"):]
	if !strings.Contains(restSection, "prometheus.io/scrape") {
		t.Fatalf("期望 Pod 模板（spec.template.metadata）的 annotations 里也有 prometheus.io/scrape，实际：\n%s", restSection)
	}
}

// ────────────────────────────────────────────────────────────────
// 用例 16：K8s 下 hostname 必填且唯一
// ────────────────────────────────────────────────────────────────

func TestPlatform16a_两组件同hostname硬报错(t *testing.T) {
	f := newFixture(t)
	f.writeFile("brickkit.yaml", `project: d16a
deploy:
  target: k8s
sources:
  - id: local-dev
    type: local
    path: ./components
components:
  - id: foo/bar
    version: 1.0.0
    expose: true
    hostname: same.example.com
  - id: foo/baz
    version: 1.0.0
    expose: true
    hostname: same.example.com
`)
	f.writeFile("components/foo/bar/component.yaml", minimalComponent("foo/bar", "1.0.0", 19001))
	f.writeFile("components/foo/baz/component.yaml", minimalComponent("foo/baz", "1.0.0", 19002))
	res := f.run("up", "--dry-run")
	if res.exitCode == 0 {
		t.Fatalf("期望两个组件撞同一个 hostname 时硬报错，实际成功了：%s", res.stdout)
	}
	if !strings.Contains(res.stdout, "same.example.com") {
		t.Fatalf("期望错误信息点出冲突的 hostname，实际输出：%s", res.stdout)
	}
}

func TestPlatform16b_expose为true漏写hostname报错(t *testing.T) {
	f := newFixture(t)
	f.writeFile("brickkit.yaml", `project: d16b
deploy:
  target: k8s
sources:
  - id: local-dev
    type: local
    path: ./components
components:
  - id: foo/bar
    version: 1.0.0
    expose: true
`)
	f.writeFile("components/foo/bar/component.yaml", minimalComponent("foo/bar", "1.0.0", 19001))
	res := f.run("up", "--dry-run")
	if res.exitCode == 0 {
		t.Fatalf("期望 K8s 下 expose:true 漏写 hostname 报错，实际成功了：%s", res.stdout)
	}
	if !strings.Contains(res.stdout, "hostname") {
		t.Fatalf("期望错误信息点出缺失的 hostname 字段，实际输出：%s", res.stdout)
	}
}
