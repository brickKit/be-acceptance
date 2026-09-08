package gates

import (
	"path/filepath"
	"testing"
)

// TestBareRouteScan_裸用gin路由方法要红 是导读第23条守的那件事：绕开了
// besdk.GET/POST(...) 的权限键签名强制，那个接口从此无人鉴权且完全
// 没有任何症状。
func TestBareRouteScan_裸用gin路由方法要红(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/erp/sales/backend/internal/http/http.go"),
		`package http

func RegisterRoutes(g *gin.RouterGroup) {
	g.GET("/orders", listHandler())
}
`)
	violations, err := BareRouteScan(root)
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

// TestBareRouteScan_besdkGET不触发 是这条扫描最容易写错的地方：
// besdk.GET(g, ...) 字面上也含有 ".GET(" 这个子串，判据不能是纯文本
// 匹配，必须看清调用的接收者是不是 besdk 包本身。
func TestBareRouteScan_besdkGET不触发(t *testing.T) {
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
	violations, err := BareRouteScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("besdk.GET/POST/PUT/PATCH/DELETE(...) 不该被判成违规，得到 %v", violations)
	}
}

// TestBareRouteScan_只扫http目录 确认这条门禁不会误伤别处同名方法
// （比如某个纯业务对象刚好也有一个叫 GET 的方法）。
func TestBareRouteScan_只扫http目录(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/erp/sales/backend/internal/repo/repo.go"),
		`package repo

func (c *httpLikeThing) GET(path string) {}
`)
	violations, err := BareRouteScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("backend/internal/http/ 之外不该被这条扫描盯，得到 %v", violations)
	}
}

// TestBareRouteScan_注释里提到GET不触发 这个代码库大量写"⚠️ 不许 X"这类
// 解释性注释，AST 解析天然跳过注释文本，不需要像 grep 方案那样先
// sed 去掉行内注释（阶段一 D5 踩过 grep 方案的这个坑）。
func TestBareRouteScan_注释里提到GET不触发(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/erp/sales/backend/internal/http/http.go"),
		`package http

import besdk "github.com/brickKit/be-sdk-go"

// ⚠️ 不许写 g.GET("/orders", ...) 这种裸调用，一律走 besdk.GET(...)。
func RegisterRoutes(g *gin.RouterGroup) {
	besdk.GET(g, "/orders", besdk.Public, listHandler())
}
`)
	violations, err := BareRouteScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("注释里的示例代码不该被判成违规，得到 %v", violations)
	}
}

// TestBareRouteScan_Python原生装饰器要红 是阶段三 Task 3 的 Python 版
// 判据：@router.get(...) 这种原生 FastAPI 装饰器绕开了 besdk.get(router,
// path, perm, handler) 的权限键强制。
func TestBareRouteScan_Python原生装饰器要红(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/infra/print/backend/app/http/routes.py"),
		`from fastapi import APIRouter

router = APIRouter()

@router.get("/templates")
async def list_templates():
    return []
`)
	violations, err := BareRouteScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 {
		t.Fatalf("期望 1 条违规，得到 %d 条：%v", len(violations), violations)
	}
	if violations[0].Method != "get" {
		t.Fatalf("方法名不对：%+v", violations[0])
	}
}

// TestBareRouteScan_Python的besdk_get不触发 确认判据是"装饰器形态"，不是
// 纯文本命中 ".get("——besdk.get(router, path, perm, handler) 是普通函数
// 调用，也常见到 dict.get(...)/环境变量.get(...) 这类完全无关的合法用法，
// 判据不能对它们也报警。
func TestBareRouteScan_Python的besdk_get不触发(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/infra/print/backend/app/http/routes.py"),
		`from besdk.authz import get as besdk_get

router = APIRouter()

def register(router):
    besdk_get(router, "/templates", "infra.print.view", list_templates)
    timeout = config.get("timeout", 30)  # 无关的 dict.get，不该被扫到
`)
	violations, err := BareRouteScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("besdk.get(...) 与 dict.get(...) 都不该被判成违规，得到 %v", violations)
	}
}

// TestBareRouteScan_TS裸resolver要红 是阶段三 Task 3 的 TS 版判据：
// GraphQL resolver 层没有"裸 gin.GET"这个概念，判据改成"resolver 是否
// 经过权限键包装"——resolver 字段的值直接是箭头函数，没有
// requirePermission(...) 包一层。
func TestBareRouteScan_TS裸resolver要红(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/infra/bff-mobile/src/resolvers/orders.ts"),
		`export const resolvers = {
  Query: {
    orders: async (_src: unknown) => {
      return [];
    },
  },
};
`)
	violations, err := BareRouteScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 {
		t.Fatalf("期望 1 条违规，得到 %d 条：%v", len(violations), violations)
	}
	if violations[0].Method != "orders" {
		t.Fatalf("字段名不对：%+v", violations[0])
	}
}

// TestBareRouteScan_TS的requirePermission不触发 确认包了一层
// requirePermission(...) 的 resolver 不会被误判。
func TestBareRouteScan_TS的requirePermission不触发(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/infra/bff-mobile/src/resolvers/orders.ts"),
		`import { requirePermission, PUBLIC } from "@brickkit/be-sdk-ts";

export const resolvers = {
  Query: {
    orders: requirePermission("erp.sales.view", async (_src: unknown) => {
      return [];
    }),
    health: requirePermission(PUBLIC, async () => "ok"),
  },
};
`)
	violations, err := BareRouteScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("requirePermission(...) 包过的 resolver 不该被判成违规，得到 %v", violations)
	}
}
