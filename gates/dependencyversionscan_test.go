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
