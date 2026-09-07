package gates

import (
	"path/filepath"
	"testing"
)

// TestBareGinScan_裸用gin路由方法要红 是导读第23条守的那件事：绕开了
// besdk.GET/POST(...) 的权限键签名强制，那个接口从此无人鉴权且完全
// 没有任何症状。
func TestBareGinScan_裸用gin路由方法要红(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/erp/sales/backend/internal/http/http.go"),
		`package http

func RegisterRoutes(g *gin.RouterGroup) {
	g.GET("/orders", listHandler())
}
`)
	violations, err := BareGinScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 {
		t.Fatalf("期望 1 条违规，得到 %d 条：%v", len(violations), violations)
	}
	if violations[0].Method != "GET" {
		t.Fatalf("方法名不对：%+v", violations[0])
	}
}

// TestBareGinScan_besdkGET不触发 是这条扫描最容易写错的地方：
// besdk.GET(g, ...) 字面上也含有 ".GET(" 这个子串，判据不能是纯文本
// 匹配，必须看清调用的接收者是不是 besdk 包本身。
func TestBareGinScan_besdkGET不触发(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/erp/sales/backend/internal/http/http.go"),
		`package http

import besdk "github.com/brickKit/be-sdk-go"

func RegisterRoutes(g *gin.RouterGroup) {
	besdk.GET(g, "/orders", besdk.Public, listHandler())
	besdk.POST(g, "/orders", besdk.Public, createHandler())
	besdk.PATCH(g, "/orders/:id", besdk.Public, updateHandler())
	besdk.DELETE(g, "/orders/:id", besdk.Public, deleteHandler())
	besdk.PUT(g, "/orders/:id", besdk.Public, replaceHandler())
}
`)
	violations, err := BareGinScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("besdk.GET/POST/PUT/PATCH/DELETE(...) 不该被判成违规，得到 %v", violations)
	}
}

// TestBareGinScan_只扫http目录 确认这条门禁不会误伤别处同名方法
// （比如某个纯业务对象刚好也有一个叫 GET 的方法）。
func TestBareGinScan_只扫http目录(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/erp/sales/backend/internal/repo/repo.go"),
		`package repo

func (c *httpLikeThing) GET(path string) {}
`)
	violations, err := BareGinScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("backend/internal/http/ 之外不该被这条扫描盯，得到 %v", violations)
	}
}

// TestBareGinScan_注释里提到GET不触发 这个代码库大量写"⚠️ 不许 X"这类
// 解释性注释，AST 解析天然跳过注释文本，不需要像 grep 方案那样先
// sed 去掉行内注释（阶段一 D5 踩过 grep 方案的这个坑）。
func TestBareGinScan_注释里提到GET不触发(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/erp/sales/backend/internal/http/http.go"),
		`package http

import besdk "github.com/brickKit/be-sdk-go"

// ⚠️ 不许写 g.GET("/orders", ...) 这种裸调用，一律走 besdk.GET(...)。
func RegisterRoutes(g *gin.RouterGroup) {
	besdk.GET(g, "/orders", besdk.Public, listHandler())
}
`)
	violations, err := BareGinScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("注释里的示例代码不该被判成违规，得到 %v", violations)
	}
}
