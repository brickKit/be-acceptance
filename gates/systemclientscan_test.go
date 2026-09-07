package gates

import (
	"path/filepath"
	"testing"
)

// TestSystemClientScan_出现在http目录里要红 是§14.2.6、导读第21条守的那件
// 事：SystemClient 用在用户请求路径上，数据权限整条被绕过，不报错，
// 返回的数据只是「多了一些」——机器不扫，人永远发现不了。
func TestSystemClientScan_出现在http目录里要红(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/erp/sales/backend/internal/http/http.go"),
		`package http

import besdk "github.com/brickKit/be-sdk-go"

func listHandler() {
	cc, _ := besdk.SystemClient("mdm/customer", "")
	_ = cc
}
`)
	violations, err := SystemClientScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 {
		t.Fatalf("期望 1 条违规，得到 %d 条：%v", len(violations), violations)
	}
	if violations[0].Component != "erp/sales" {
		t.Fatalf("违规组件不对：%+v", violations[0])
	}
}

func TestSystemClientScan_出现在grpc目录里也要红(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/erp/sales/backend/internal/grpc/grpc.go"),
		`package grpc

import besdk "github.com/brickKit/be-sdk-go"

func (s *server) Get() {
	cc, _ := besdk.SystemClient("mdm/customer", "")
	_ = cc
}
`)
	violations, err := SystemClientScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 {
		t.Fatalf("期望 1 条违规，得到 %d 条：%v", len(violations), violations)
	}
}

// TestSystemClientScan_出现在module目录里放行 验证 Start() 所在的
// backend/module/ 不受这条扫描管——那是它唯一的合法出现位置之一。
func TestSystemClientScan_出现在module目录里放行(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/erp/sales/backend/module/module.go"),
		`package module

import besdk "github.com/brickKit/be-sdk-go"

func Start() {
	cc, _ := besdk.SystemClient("mdm/customer", "")
	_ = cc
}
`)
	violations, err := SystemClientScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("backend/module/ 不该被这条扫描盯，得到 %v", violations)
	}
}

// TestSystemClientScan_UserClient不触发 确认扫描认的是函数名而不是
// 「凡是 besdk 的 client 都可疑」——UserClient 本来就该出现在请求路径上。
func TestSystemClientScan_UserClient不触发(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/erp/sales/backend/internal/http/http.go"),
		`package http

import besdk "github.com/brickKit/be-sdk-go"

func listHandler(ctx context.Context) {
	cc, _ := besdk.UserClient(ctx, "mdm/customer", "")
	_ = cc
}
`)
	violations, err := SystemClientScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("UserClient 不该被这条扫描当成违规，得到 %v", violations)
	}
}
