package versionbump

import (
	"strings"
	"testing"
)

func TestBumpPatch_基本递增(t *testing.T) {
	got, err := BumpPatch("1.0.11")
	if err != nil {
		t.Fatal(err)
	}
	if got != "1.0.12" {
		t.Fatalf("期望 1.0.12，实际 %s", got)
	}
}

func TestBumpPatch_两位数进位不影响前两段(t *testing.T) {
	got, err := BumpPatch("1.0.9")
	if err != nil {
		t.Fatal(err)
	}
	if got != "1.0.10" {
		t.Fatalf("期望 1.0.10，实际 %s", got)
	}
}

func TestBumpPatch_格式不对时报错(t *testing.T) {
	if _, err := BumpPatch("1.0"); err == nil {
		t.Fatal("期望报错，实际没有")
	}
	if _, err := BumpPatch("1.0.x"); err == nil {
		t.Fatal("期望报错，实际没有")
	}
}

func testRegistry() map[string]*Component {
	return map[string]*Component{
		"mdm/customer":    {ID: "mdm/customer", Version: "1.0.5"},
		"mdm/product":     {ID: "mdm/product", Version: "1.0.6"},
		"erp/inventory":   {ID: "erp/inventory", Version: "1.0.12", DepIDs: nil},
		"erp/finance":     {ID: "erp/finance", Version: "1.0.8", DepIDs: nil},
		"erp/sales":       {ID: "erp/sales", Version: "1.0.17", DepIDs: []string{"mdm/customer", "mdm/product", "erp/inventory", "erp/finance", "infra/workflow"}},
		"infra/workflow":  {ID: "infra/workflow", Version: "1.0.2"},
		"infra/bff-mobile": {ID: "infra/bff-mobile", Version: "1.0.11", DepIDs: []string{"mdm/customer", "mdm/product", "erp/sales", "erp/inventory", "infra/workflow"}},
		"crm/opportunity": {ID: "crm/opportunity", Version: "1.0.6", DepIDs: []string{"mdm/customer", "mdm/product"}},
	}
}

// TestProperty_单个根变更三层级联全部覆盖 复现这次真实排查的场景：改了
// erp/inventory，erp/sales（直接依赖它）跟着变，infra/bff-mobile（依赖
// erp/sales，不直接依赖 erp/inventory，但也间接受影响——本用例里
// infra/bff-mobile 本身也直接列了 erp/inventory 依赖）也跟着变，
// crm/opportunity（完全不依赖 erp/inventory）不该被牵连。
func TestComputeCascade_三层级联全部覆盖且无关组件不受影响(t *testing.T) {
	reg := testRegistry()
	changes, err := ComputeCascade(reg, []SeedChange{
		{ID: "erp/inventory", Reason: "补属性测试，无行为/契约变更。"},
	})
	if err != nil {
		t.Fatal(err)
	}

	byID := map[string]Change{}
	for _, c := range changes {
		byID[c.ID] = c
	}

	if _, ok := byID["crm/opportunity"]; ok {
		t.Fatal("crm/opportunity 不依赖 erp/inventory，不该被牵连")
	}

	inv, ok := byID["erp/inventory"]
	if !ok || inv.IsCascade {
		t.Fatalf("erp/inventory 应该是根变更，实际 %+v ok=%v", inv, ok)
	}
	if inv.NewVer != "1.0.13" {
		t.Fatalf("期望 erp/inventory 新版本 1.0.13，实际 %s", inv.NewVer)
	}

	sales, ok := byID["erp/sales"]
	if !ok || !sales.IsCascade {
		t.Fatalf("erp/sales 应该被级联牵连，实际 %+v ok=%v", sales, ok)
	}
	if sales.NewVer != "1.0.18" {
		t.Fatalf("期望 erp/sales 新版本 1.0.18，实际 %s", sales.NewVer)
	}

	bff, ok := byID["infra/bff-mobile"]
	if !ok || !bff.IsCascade {
		t.Fatalf("infra/bff-mobile 应该被级联牵连，实际 %+v ok=%v", bff, ok)
	}
	if bff.NewVer != "1.0.12" {
		t.Fatalf("期望 infra/bff-mobile 新版本 1.0.12，实际 %s", bff.NewVer)
	}
}

// TestComputeCascade_一个组件被两个刚变更的依赖同时牵连只处理一次
// 复现 erp-sales 同时依赖 erp/inventory 与 erp/finance 的真实场景——两者
// 同一批一起改时，erp-sales 应该只出现一次，理由里把两条依赖都点名。
func TestComputeCascade_一个组件被两个刚变更的依赖同时牵连只处理一次(t *testing.T) {
	reg := testRegistry()
	changes, err := ComputeCascade(reg, []SeedChange{
		{ID: "erp/inventory", Reason: "r1"},
		{ID: "erp/finance", Reason: "r2"},
	})
	if err != nil {
		t.Fatal(err)
	}

	count := 0
	var salesReason string
	for _, c := range changes {
		if c.ID == "erp/sales" {
			count++
			salesReason = c.Reason
		}
	}
	if count != 1 {
		t.Fatalf("erp/sales 期望只出现一次，实际出现 %d 次", count)
	}
	if !strings.Contains(salesReason, "erp/inventory@1.0.13") || !strings.Contains(salesReason, "erp/finance@1.0.9") {
		t.Fatalf("erp/sales 的理由应该同时点名两条依赖，实际：%s", salesReason)
	}
}

// TestComputeCascade_根变更理由的换行会被压成单行 是真机验证 bump-version
// 时才发现的真实 bug：ParsePlanFile 允许 reason 字段跨多行写（方便在
// 计划文件里排版），但这段文字最终要塞进 component.yaml 的单行注释
// 里，原样带着换行符写进去会把后续内容变成裸露的 YAML、直接撞坏文件。
// 在写盘之前，ComputeCascade 就该把它压成单行。
func TestComputeCascade_根变更理由的换行会被压成单行(t *testing.T) {
	reg := testRegistry()
	changes, err := ComputeCascade(reg, []SeedChange{
		{ID: "erp/inventory", Reason: "第一行理由，\n  第二行理由，\n\t第三行理由。"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(changes[0].Reason, "\n") {
		t.Fatalf("理由里不该再有换行符，实际：%q", changes[0].Reason)
	}
	// 纯中文断行之间不该插空格（中文本身词与词之间不加空格）。
	want := "第一行理由，第二行理由，第三行理由。"
	if changes[0].Reason != want {
		t.Fatalf("期望压成 %q，实际 %q", want, changes[0].Reason)
	}
}

// TestComputeCascade_理由换行在中英文边界正确保留空格 是
// TestComputeCascade_根变更理由的换行会被压成单行 的补充：断在纯中文
// 片段之间不该插空格，但断在英文术语（test-cross）跟中文之间必须保留
// 空格，否则会拼成"真实test-cross桥接"这种既有排版习惯里不允许的形状。
func TestComputeCascade_理由换行在中英文边界正确保留空格(t *testing.T) {
	reg := testRegistry()
	changes, err := ComputeCascade(reg, []SeedChange{
		{ID: "erp/inventory", Reason: "真实\ntest-cross\n桥接的容器。"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "真实 test-cross 桥接的容器。"
	if changes[0].Reason != want {
		t.Fatalf("期望 %q，实际 %q", want, changes[0].Reason)
	}
}

func TestComputeCascade_显式目标版本覆盖patch自增(t *testing.T) {
	reg := testRegistry()
	changes, err := ComputeCascade(reg, []SeedChange{
		{ID: "erp/inventory", Reason: "破坏性变更走 minor", NewVer: "1.1.0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if changes[0].NewVer != "1.1.0" {
		t.Fatalf("期望显式版本 1.1.0 生效，实际 %s", changes[0].NewVer)
	}
}

func TestComputeCascade_引用不存在的组件报错(t *testing.T) {
	reg := testRegistry()
	if _, err := ComputeCascade(reg, []SeedChange{{ID: "erp/no-such-component", Reason: "x"}}); err == nil {
		t.Fatal("期望报错，实际没有")
	}
}

func TestComputeCascade_同一组件出现两次根变更报错(t *testing.T) {
	reg := testRegistry()
	if _, err := ComputeCascade(reg, []SeedChange{
		{ID: "erp/inventory", Reason: "x"},
		{ID: "erp/inventory", Reason: "y"},
	}); err == nil {
		t.Fatal("期望报错，实际没有")
	}
}
