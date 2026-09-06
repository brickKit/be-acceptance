package gates

import (
	"os"
	"path/filepath"
	"testing"
)

func TestImportScan_组件之间互相import要红(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/erp/sales/go.mod"),
		"module github.com/brickKit/erp-sales\n\ngo 1.25\n")
	write(t, filepath.Join(root, "components/erp/sales/svc.go"),
		`package svc

import (
	"github.com/brickKit/be-sdk-go"          // 白名单，放行
	"github.com/brickKit/mdm-customer/model" // ❌ 指向另一个组件仓库
)
`)
	write(t, filepath.Join(root, "components/mdm/customer/go.mod"),
		"module github.com/brickKit/mdm-customer\n\ngo 1.25\n")

	violations, err := ImportScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 {
		t.Fatalf("期望 1 条违规，得到 %d 条：%v", len(violations), violations)
	}
	if violations[0].From != "erp/sales" || violations[0].To != "mdm/customer" {
		t.Fatalf("违规方向不对：%+v", violations[0])
	}
}

func TestImportScan_引be_sdk放行(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/erp/sales/go.mod"),
		"module github.com/brickKit/erp-sales\n\ngo 1.25\n")
	write(t, filepath.Join(root, "components/erp/sales/svc.go"),
		`package svc

import "github.com/brickKit/be-sdk-go"
`)
	violations, err := ImportScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("be-sdk-go 在白名单里，应放行，得到 %v", violations)
	}
}

// 也不许抽一个「公共 model 包」给两个组件共用——
// 契约在 contracts/，代码不共享（§13.3 铁律六）
func TestImportScan_公共model包也要红(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/erp/sales/go.mod"),
		"module github.com/brickKit/erp-sales\n\ngo 1.25\n")
	write(t, filepath.Join(root, "components/erp/sales/svc.go"),
		`package svc

import "github.com/brickKit/be-common-model"
`)
	violations, err := ImportScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 {
		t.Fatalf("be-common-model 不在白名单里，应报违规，得到 %v", violations)
	}
}

func TestImportScan_自己内部子包不算违规(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/erp/sales/go.mod"),
		"module github.com/brickKit/erp-sales\n\ngo 1.25\n")
	write(t, filepath.Join(root, "components/erp/sales/svc.go"),
		`package svc

import "github.com/brickKit/erp-sales/internal/model"
`)
	violations, err := ImportScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("自己 module 内部的子包不该算违规，得到 %v", violations)
	}
}

func TestImportScan_跳过archived目录(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/erp/sales/go.mod"),
		"module github.com/brickKit/erp-sales\n\ngo 1.25\n")
	write(t, filepath.Join(root, "components/erp/sales/svc.go"),
		`package svc

import "github.com/brickKit/be-sdk-go"
`)
	// .archived 下的目录不是 root/components/<scope>/<name> 形态（多一层
	// 归档前缀），componentDirs 天然不会把它当组件目录——这里放一份会立刻
	// 触发违规的坏代码，确保它真的没被扫到。
	write(t, filepath.Join(root, "components/.archived/erp/old-sales/go.mod"),
		"module github.com/brickKit/erp-old-sales\n\ngo 1.25\n")
	write(t, filepath.Join(root, "components/.archived/erp/old-sales/svc.go"),
		`package svc

import "github.com/brickKit/mdm-customer/model"
`)

	violations, err := ImportScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf(".archived/ 不该被扫描，得到 %v", violations)
	}
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}
