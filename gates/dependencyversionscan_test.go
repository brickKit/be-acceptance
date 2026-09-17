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

func TestDependencyVersionScan_全部同步时零违规(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/mdm/customer/component.yaml"),
		componentYaml("mdm/customer", "1.0.4"))
	write(t, filepath.Join(root, "components/erp/sales/component.yaml"),
		"apiVersion: brickkit/v1\nkind: Component\n\n"+
			"metadata:\n  id: erp/sales\n  name: 销售订单\n  version: 1.0.10\n  description: x\n\n"+
			"dependencies:\n  components:\n    - mdm/customer@1.0.4\n  resources: []\n")
	write(t, filepath.Join(root, "brickkit.yaml"),
		"project: x\ncomponents:\n"+
			"  - id: mdm/customer\n    version: 1.0.4 # 注释\n"+
			"  - id: erp/sales\n    version: 1.0.10 # 注释\n"+
			"resources:\n  - id: postgres-shared\n")

	mismatches, err := DependencyVersionScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(mismatches) != 0 {
		t.Fatalf("期望 0 条违规，得到：%+v", mismatches)
	}
}

func TestDependencyVersionScan_组件引用落后于依赖方真实版本(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/mdm/customer/component.yaml"),
		componentYaml("mdm/customer", "1.0.4"))
	// erp-sales 还停在旧版本引用（C16 复现场景）。
	write(t, filepath.Join(root, "components/erp/sales/component.yaml"),
		"apiVersion: brickkit/v1\nkind: Component\n\n"+
			"metadata:\n  id: erp/sales\n  name: 销售订单\n  version: 1.0.9\n  description: x\n\n"+
			"dependencies:\n  components:\n    - mdm/customer@1.0.3\n  resources: []\n")

	mismatches, err := DependencyVersionScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(mismatches) != 1 {
		t.Fatalf("期望 1 条违规，得到 %d 条：%+v", len(mismatches), mismatches)
	}
	got := mismatches[0]
	if got.Declarer != "erp/sales" || got.Dependency != "mdm/customer" ||
		got.DeclaredVersion != "1.0.3" || got.ActualVersion != "1.0.4" {
		t.Fatalf("违规内容不对：%+v", got)
	}
}

func TestDependencyVersionScan_对象形式弱依赖同样被检查(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/infra/workflow/component.yaml"),
		componentYaml("infra/workflow", "1.0.1"))
	write(t, filepath.Join(root, "components/infra/bff-mobile/component.yaml"),
		"apiVersion: brickkit/v1\nkind: Component\n\n"+
			"metadata:\n  id: infra/bff-mobile\n  name: 移动端BFF\n  version: 1.0.4\n  description: x\n\n"+
			"dependencies:\n  components:\n"+
			"    - { id: infra/workflow@1.0.0, optional: true }\n  resources: []\n")

	mismatches, err := DependencyVersionScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(mismatches) != 1 {
		t.Fatalf("期望 1 条违规，得到 %d 条：%+v", len(mismatches), mismatches)
	}
	got := mismatches[0]
	if got.Declarer != "infra/bff-mobile" || got.Dependency != "infra/workflow" ||
		got.DeclaredVersion != "1.0.0" || got.ActualVersion != "1.0.1" {
		t.Fatalf("违规内容不对：%+v", got)
	}
}

func TestDependencyVersionScan_brickkit顶层pin落后(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/mdm/customer/component.yaml"),
		componentYaml("mdm/customer", "1.0.4"))
	write(t, filepath.Join(root, "brickkit.yaml"),
		"project: x\ncomponents:\n"+
			"  - id: mdm/customer\n    version: 1.0.3 # 顶层 pin 落后于组件自己已发布的新版本\n"+
			"resources:\n  - id: postgres-shared\n")

	mismatches, err := DependencyVersionScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(mismatches) != 1 {
		t.Fatalf("期望 1 条违规，得到 %d 条：%+v", len(mismatches), mismatches)
	}
	got := mismatches[0]
	if got.Declarer != "brickkit.yaml" || got.Dependency != "mdm/customer" ||
		got.DeclaredVersion != "1.0.3" || got.ActualVersion != "1.0.4" {
		t.Fatalf("违规内容不对：%+v", got)
	}
}

func TestDependencyVersionScan_外壳go点mod锁定版本落后(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/mdm/customer/component.yaml"),
		componentYaml("mdm/customer", "1.0.9"))
	// shells/go/go.mod 锁定的是旧版本——05b Task 4c 真机复现的真实场景。
	write(t, filepath.Join(root, "shells/go/go.mod"),
		"module github.com/brickKit/be-shell-go\n\ngo 1.25.11\n\nrequire (\n"+
			"\tgithub.com/brickKit/be-sdk-go v0.2.7\n"+
			"\tgithub.com/brickKit/mdm-customer v1.0.7\n"+
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
	if got.Declarer != "shells/go/go.mod" || got.Dependency != "mdm/customer" ||
		got.DeclaredVersion != "1.0.7" || got.ActualVersion != "1.0.9" {
		t.Fatalf("违规内容不对：%+v", got)
	}
}

func TestDependencyVersionScan_外壳go点mod的gen子模块和间接依赖不参与比对(t *testing.T) {
	root := t.TempDir()
	// mdm/customer 自己版本是 1.0.9，go.mod 顶层引用也是 1.0.9（同步），
	// 但 gen/mdm/customer 子模块版本落后（1.0.6）——这是刻意的独立发布
	// 节奏（设计书 §13.3 铁律六第二类白名单），不该被这个门禁当成漂移。
	write(t, filepath.Join(root, "components/mdm/customer/component.yaml"),
		componentYaml("mdm/customer", "1.0.9"))
	write(t, filepath.Join(root, "shells/go/go.mod"),
		"module github.com/brickKit/be-shell-go\n\ngo 1.25.11\n\nrequire (\n"+
			"\tgithub.com/brickKit/mdm-customer v1.0.9\n"+
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
	// 05b 真机复测时真实漏改过的场景：只 bump 了 metadata.version，
	// deployment.image 停在旧 tag。
	write(t, filepath.Join(root, "shells/go/deploy/shell/go-core/component.yaml"),
		"apiVersion: brickkit/v1\nkind: Component\n\n"+
			"metadata:\n  id: shell/go-core\n  name: x\n  version: 0.5.6\n  description: x\n\n"+
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

func TestDependencyVersionScan_引用不存在的组件不报违规(t *testing.T) {
	root := t.TempDir()
	// erp-sales 引用了一个本仓库根本没有的组件 ID——查不出真实版本，
	// 静默跳过，不是这个门禁的职责范围。
	write(t, filepath.Join(root, "components/erp/sales/component.yaml"),
		"apiVersion: brickkit/v1\nkind: Component\n\n"+
			"metadata:\n  id: erp/sales\n  name: 销售订单\n  version: 1.0.10\n  description: x\n\n"+
			"dependencies:\n  components:\n    - mdm/nonexistent@1.0.0\n  resources: []\n")

	mismatches, err := DependencyVersionScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(mismatches) != 0 {
		t.Fatalf("期望 0 条违规，得到：%+v", mismatches)
	}
}
