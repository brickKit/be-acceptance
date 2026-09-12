package versionbump

import "testing"

func TestParsePlanFile_基本两块(t *testing.T) {
	seeds, err := ParsePlanFile(`id: erp/inventory
reason: 补属性测试，无行为/契约变更。
---
id: erp/finance
reason: 补属性测试，无行为/契约变更。
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(seeds) != 2 {
		t.Fatalf("期望 2 块，实际 %d", len(seeds))
	}
	if seeds[0].ID != "erp/inventory" || seeds[0].Reason != "补属性测试，无行为/契约变更。" {
		t.Fatalf("第一块解析不对：%+v", seeds[0])
	}
	if seeds[1].ID != "erp/finance" {
		t.Fatalf("第二块解析不对：%+v", seeds[1])
	}
}

func TestParsePlanFile_reason跨多行(t *testing.T) {
	seeds, err := ParsePlanFile(`id: erp/sales
reason: 第一行理由，
  第二行理由，
  第三行理由。
`)
	if err != nil {
		t.Fatal(err)
	}
	want := "第一行理由，\n  第二行理由，\n  第三行理由。"
	if seeds[0].Reason != want {
		t.Fatalf("多行 reason 拼接不对，期望 %q，实际 %q", want, seeds[0].Reason)
	}
}

func TestParsePlanFile_显式version字段(t *testing.T) {
	seeds, err := ParsePlanFile(`id: erp/inventory
version: 1.1.0
reason: 破坏性变更走 minor。
`)
	if err != nil {
		t.Fatal(err)
	}
	if seeds[0].NewVer != "1.1.0" {
		t.Fatalf("期望显式版本 1.1.0，实际 %q", seeds[0].NewVer)
	}
}

func TestParsePlanFile_缺id报错(t *testing.T) {
	if _, err := ParsePlanFile("reason: 没有 id\n"); err == nil {
		t.Fatal("期望报错，实际没有")
	}
}

func TestParsePlanFile_缺reason报错(t *testing.T) {
	if _, err := ParsePlanFile("id: erp/inventory\n"); err == nil {
		t.Fatal("期望报错，实际没有")
	}
}

func TestParsePlanFile_空文件报错(t *testing.T) {
	if _, err := ParsePlanFile("   \n\n"); err == nil {
		t.Fatal("期望报错，实际没有")
	}
}

func TestParsePlanFile_看不懂的行报错(t *testing.T) {
	if _, err := ParsePlanFile("id: erp/inventory\nthis is garbage\nreason: x\n"); err == nil {
		t.Fatal("期望报错，实际没有")
	}
}
