package gates

import (
	"path/filepath"
	"testing"
)

func componentYaml(id, version string) string {
	return "apiVersion: brickkit/v1\n" +
		"kind: Component\n\n" +
		"metadata:\n" +
		"  id: " + id + "\n" +
		"  name: 测试组件\n" +
		"  version: " + version + " # 精确版本\n" +
		"  description: 仅供测试\n\n" +
		"dependencies:\n" +
		"  components: []\n" +
		"  resources: []\n"
}

// v1 布局：外壳在 shell/be/<name>/，go.mod 的 require 对 v2+ 带 /vN 模块路径后缀。

func TestDependencyVersionScan_全部同步时零违规(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/mdm/customer/component.yaml"),
		componentYaml("mdm/customer", "2.0.1"))
	write(t, filepath.Join(root, "shell/be/go-core/go.mod"),
		"module github.com/brickKit/be-shell-go\n\ngo 1.25.11\n\nrequire (\n"+
			"\tgithub.com/brickKit/be-sdk-go v0.2.7\n"+
			"\tgithub.com/brickKit/mdm-customer/v2 v2.0.1\n"+
			")\n")
	write(t, filepath.Join(root, "shell/be/go-core/component.yaml"),
		"apiVersion: brickkit/v1\nkind: Component\n\n"+
			"metadata:\n  id: be/go-core\n  name: x\n  version: 0.5.6\n  description: x\n\n"+
			"deployment:\n  type: container\n  image: brickenterprise/be-shell-go:0.5.6\n  port: 8090\n")

	mismatches, err := DependencyVersionScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(mismatches) != 0 {
		t.Fatalf("期望 0 条违规，得到：%+v", mismatches)
	}
}

func TestDependencyVersionScan_外壳go点mod锁定版本落后(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/mdm/customer/component.yaml"),
		componentYaml("mdm/customer", "2.0.9"))
	// go.mod 锁定的是旧版本——05b Task 4c 真机复现的真实场景，v1 的 /v2 路径形式。
	write(t, filepath.Join(root, "shell/be/go-core/go.mod"),
		"module github.com/brickKit/be-shell-go\n\ngo 1.25.11\n\nrequire (\n"+
			"\tgithub.com/brickKit/be-sdk-go v0.2.7\n"+
			"\tgithub.com/brickKit/mdm-customer/v2 v2.0.7\n"+
			"\tgithub.com/brickKit/mdm-customer/gen/mdm/customer v1.0.6\n"+
			")\n\nrequire (\n"+
			"\tgithub.com/beorn7/perks v1.0.1 // indirect\n"+
			")\n")

	mismatches, err := DependencyVersionScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(mismatches) != 1 {
		t.Fatalf("期望 1 条违规，得到 %d 条：%+v", len(mismatches), mismatches)
	}
	got := mismatches[0]
	if got.Declarer != "shell/be/go-core/go.mod" || got.Dependency != "mdm/customer" ||
		got.DeclaredVersion != "2.0.7" || got.ActualVersion != "2.0.9" {
		t.Fatalf("违规内容不对：%+v", got)
	}
}

func TestDependencyVersionScan_v1及以下无版本后缀的模块路径同样被检查(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/mdm/customer/component.yaml"),
		componentYaml("mdm/customer", "1.0.9"))
	write(t, filepath.Join(root, "shell/be/go-core/go.mod"),
		"module x\n\nrequire (\n\tgithub.com/brickKit/mdm-customer v1.0.7\n)\n")

	mismatches, err := DependencyVersionScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(mismatches) != 1 || mismatches[0].DeclaredVersion != "1.0.7" || mismatches[0].ActualVersion != "1.0.9" {
		t.Fatalf("期望 1 条 1.0.7→1.0.9 的违规，得到：%+v", mismatches)
	}
}

func TestDependencyVersionScan_外壳go点mod的gen子模块和间接依赖不参与比对(t *testing.T) {
	root := t.TempDir()
	// gen/mdm/customer 子模块版本落后是刻意的独立发布节奏（设计书 §13.3
	// 铁律六第二类白名单），不该被当成漂移。
	write(t, filepath.Join(root, "components/mdm/customer/component.yaml"),
		componentYaml("mdm/customer", "2.0.9"))
	write(t, filepath.Join(root, "shell/be/go-core/go.mod"),
		"module github.com/brickKit/be-shell-go\n\ngo 1.25.11\n\nrequire (\n"+
			"\tgithub.com/brickKit/mdm-customer/v2 v2.0.9\n"+
			"\tgithub.com/brickKit/mdm-customer/gen/mdm/customer v1.0.6\n"+
			")\n")

	mismatches, err := DependencyVersionScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(mismatches) != 0 {
		t.Fatalf("期望 0 条违规（gen 子模块版本独立、不参与比对），得到：%+v", mismatches)
	}
}

func TestDependencyVersionScan_外壳自己的version与deployment点image镜像tag不一致(t *testing.T) {
	root := t.TempDir()
	// 只 bump 了 metadata.version，deployment.image 停在旧 tag——
	// 实验记录（task10）确认 brickkit v1 的 lint/up --dry-run 都不查这个。
	write(t, filepath.Join(root, "shell/be/go-core/component.yaml"),
		"apiVersion: brickkit/v1\nkind: Component\n\n"+
			"metadata:\n  id: be/go-core\n  name: x\n  version: 0.5.6\n  description: x\n\n"+
			"deployment:\n  type: container\n  image: brickenterprise/be-shell-go:0.5.5\n  port: 8090\n")

	mismatches, err := DependencyVersionScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(mismatches) != 1 {
		t.Fatalf("期望 1 条违规，得到 %d 条：%+v", len(mismatches), mismatches)
	}
	got := mismatches[0]
	if got.DeclaredVersion != "0.5.6" || got.ActualVersion != "0.5.5" {
		t.Fatalf("违规内容不对：%+v", got)
	}
}

// 组件间依赖引用、brickkit.yaml pin、shell.members 的漂移由
// `brickkit up --dry-run` 拦截（实验记录 task10），这个 gate 不再重复检查。
func TestDependencyVersionScan_不再检查组件依赖引用与brickkit点yaml_pin(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/mdm/customer/component.yaml"),
		componentYaml("mdm/customer", "2.0.4"))
	write(t, filepath.Join(root, "components/erp/sales/component.yaml"),
		"apiVersion: brickkit/v1\nkind: Component\n\n"+
			"metadata:\n  id: erp/sales\n  name: 销售订单\n  version: 2.0.9\n  description: x\n\n"+
			"dependencies:\n  components:\n    - mdm/customer@2.0.3\n  resources: []\n")
	write(t, filepath.Join(root, "brickkit.yaml"),
		"project: x\ncomponents:\n  - id: mdm/customer\n    version: 2.0.3\n")

	mismatches, err := DependencyVersionScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(mismatches) != 0 {
		t.Fatalf("期望 0 条违规（交给 brickkit up --dry-run），得到：%+v", mismatches)
	}
}

func TestDependencyVersionScan_引用不存在的组件不报违规(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "shell/be/go-core/go.mod"),
		"module x\n\nrequire (\n\tgithub.com/brickKit/mdm-nonexistent/v2 v2.0.0\n)\n")

	mismatches, err := DependencyVersionScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(mismatches) != 0 {
		t.Fatalf("期望 0 条违规，得到：%+v", mismatches)
	}
}
