// Package tier2_test 固化档 2 出档的合并态专属断言（阶段四 Task 11）——
// 被测对象是"合并部署这一刻磨掉了多少组件性"，不是 tier0（真实业务闭环）
// 也不是 tier1（brickKit 自身行为）。
//
// 本文件只覆盖 Task 11 两条要求里的后一条："共享连接池下 SET LOCAL
// 越权测试"。前一条"单个模块 panic 不拖垮外壳其余模块"物理上做不到放在
// 这里——它要真的调用 shells/go 的 internal/shell.Run，那是另一个 Go
// module 自己的 internal 包，跨 module 天然不可见（Go 编译器层面就不
// 允许），跟本仓库铁律六的 import-scan 门禁无关，是更硬的语言限制。那条
// 测试落在 shells/go/internal/shell/real_modules_test.go
// （TestRun_一个模块panic不影响其它真实模块继续服务），`make tier2`
// 会同时跑这两处，见根 Makefile 与本仓库 Makefile 的 tier2 目标。
//
// 本文件反而可以只用 be-sdk-go（铁律六白名单例外）+ 一个真实的
// TEST_PG_DSN 连接就做到——不需要 import 任何组件仓库。
package tier2_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"

	besdk "github.com/brickKit/be-sdk-go"
	_ "github.com/jackc/pgx/v5/stdlib" // 注册 "pgx" 驱动，同全项目既有约定
)

// TestTier2_共享连接池下漏加schema限定仍只读到自己schema的数据 验证设计书
// §13.3 铁律二点名的"最难查的一个坑"在真实的、合并部署会用到的共享连接池
// 机制（besdk.WithTx 的 SET LOCAL ROLE + SET LOCAL search_path）下，即使
// 组件代码里的查询语句本身漏加了 schema 限定（只写表名，不写
// "schema.表名"），也不会因为共享池复用了别的模块用过的物理连接而读到
// 邻居模块的数据。
//
// ⚠️ 这条测试特意不满足于"查询失败/报错"（那只需要不同 schema 之间权限
// 隔离就够了，registry/schemas.tsv 生成的建库脚本 GRANT 本来就只给每个
// 组件角色自己 schema 的权限，跨 schema 天然 permission denied）——真正
// 要验证的是任务原文那句"确认读到的是自己 schema 的数据而不是隔壁模块的"，
// 这就要求两个不同模块的 schema 里存在同名表，不区分 schema 限定的查询
// 才有"到底会读到哪一份"这个真正有意义的问题。mdm_customer/mdm_product
// 两个真实、零依赖的组件在真实迁移里当然不会凑巧同名，这里用测试自己
// 建的临时探针表（跑完即删，不留痕迹）人为制造这个同名场景。
//
// 用真实并发（而不是顺序交替）+ 一个人为收窄到 2 条的连接池上限，逼真实的
// database/sql 连接池在两个角色之间来回复用同一条物理连接——这正是
// "共享连接池"这四个字字面要测的场景，顺序执行测不出连接复用间隙里可能
// 出现的越权。
func TestTier2_共享连接池下漏加schema限定仍只读到自己schema的数据(t *testing.T) {
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("未设置 TEST_PG_DSN，跳过（需要真实可达的 brickkit_test_db，先 make test-db-init）")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("打开 TEST_PG_DSN 失败: %v", err)
	}
	// ⚠️ 真实踩过的坑：这里不能用裸 defer db.Close()——t.Cleanup 注册的
	// 探针表 DROP（setupProbeSchema 内部）在测试函数体 return 之后才执行，
	// 而普通 defer 在函数体 return 时就先跑完了，先于 t.Cleanup。裸 defer
	// 会导致 DROP TABLE 在一个已经关闭的连接池上执行、静默失败（DROP 那
	// 一行故意忽略了 error），探针表全部真实残留在 brickkit_test_db 里
	// ——写完这条测试用真机验证 t.Cleanup 是否生效时才发现，不是纸面推演
	// 出来的。这里也用 t.Cleanup（在两个 setupProbeSchema 调用之前注册，
	// LIFO 顺序下最后关闭），保证 DROP 先于 Close 执行。
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(2) // 关键：逼真实的连接池在两个角色之间反复复用同一条物理连接

	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		t.Skipf("TEST_PG_DSN 连不上（%v），跳过", err)
	}

	requireRealRole(t, db, "mdm_customer_rw")
	requireRealRole(t, db, "mdm_product_rw")

	const probeTable = "zz_tier2_schema_boundary_probe"
	setupProbeSchema(t, db, "mdm_customer", "mdm_customer_rw", probeTable, "mdm_customer-独有标记")
	setupProbeSchema(t, db, "mdm_product", "mdm_product_rw", probeTable, "mdm_product-独有标记")

	type probe struct {
		role, schema, wantMarker string
	}
	probes := []probe{
		{"mdm_customer_rw", "mdm_customer", "mdm_customer-独有标记"},
		{"mdm_product_rw", "mdm_product", "mdm_product-独有标记"},
	}

	const rounds = 30 // 每个角色各跑 30 次，制造真实的连接复用交替
	var wg sync.WaitGroup
	errs := make(chan error, rounds*len(probes))
	for i := 0; i < rounds; i++ {
		for _, p := range probes {
			p := p
			wg.Add(1)
			go func() {
				defer wg.Done()
				// ⚠️ 故意漏加 schema 限定——不写 "schema.表名"，只写裸表名，
				// 完全依赖 WithTx 内部 SET LOCAL search_path 生效，模拟
				// 组件代码里忘记显式限定 schema 这个真实会发生的疏忽。
				err := besdk.WithTx(ctx, db, p.role, p.schema, func(tx *sql.Tx) error {
					var got string
					if err := tx.QueryRowContext(ctx,
						fmt.Sprintf("SELECT marker FROM %s", probeTable)).Scan(&got); err != nil {
						return fmt.Errorf("角色 %s 查询未加 schema 限定的 %s 失败: %w", p.role, probeTable, err)
					}
					if got != p.wantMarker {
						return fmt.Errorf("越权！角色 %s（应该只能看到 %q）实际读到了 %q——共享连接池下 SET LOCAL 没有正确隔离",
							p.role, p.wantMarker, got)
					}
					return nil
				})
				if err != nil {
					errs <- err
				}
			}()
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// requireRealRole 确认 registry/schemas.tsv 声明的组件角色真的存在于
// TEST_PG_DSN 指向的库里——不存在说明 make test-db-init 没跑过或跑的是
// 旧版本，跳过而不是报错（同项目里"缺真实外部前提就跳过，不是放宽断言"
// 的既有判据）。
func requireRealRole(t *testing.T, db *sql.DB, role string) {
	t.Helper()
	var exists bool
	err := db.QueryRow("SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", role).Scan(&exists)
	if err != nil {
		t.Fatalf("查询角色 %s 是否存在失败: %v", role, err)
	}
	if !exists {
		t.Skipf("角色 %s 不存在（先 make test-db-init）", role)
	}
}

// setupProbeSchema 在指定 schema 下建一张同名探针表、塞一行带唯一标记的
// 数据，t.Cleanup 时删干净——不使用真实组件迁移建的表，不污染真实业务
// schema 的表结构,也不依赖任何组件恰好有同名表这种偶然性。
//
// ⚠️ 显式 GRANT 给 role，不依赖 ALTER DEFAULT PRIVILEGES 是否已经覆盖这张
// 后建的表——dbscript/gen.go 产出的 ALTER DEFAULT PRIVILEGES 只对"之后
// 由同一个 grantor（这里连接用的 postgres 超级用户）新建的表"生效，理论上
// 这张探针表也该被覆盖到，但显式 GRANT 一次成本几乎为零，不需要为了省
// 一行代码去依赖这条隐含前提是否成立。
func setupProbeSchema(t *testing.T, db *sql.DB, schema, role, table, marker string) {
	t.Helper()
	ctx := context.Background()
	qualified := schema + "." + table
	if _, err := db.ExecContext(ctx, fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s (marker text NOT NULL)", qualified)); err != nil {
		t.Fatalf("建探针表 %s 失败: %v", qualified, err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("GRANT SELECT ON %s TO %s", qualified, role)); err != nil {
		t.Fatalf("GRANT SELECT ON %s TO %s 失败: %v", qualified, role, err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("TRUNCATE %s", qualified)); err != nil {
		t.Fatalf("清空探针表 %s 失败: %v", qualified, err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf(
		"INSERT INTO %s (marker) VALUES ($1)", qualified), marker); err != nil {
		t.Fatalf("写入探针数据 %s 失败: %v", qualified, err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), fmt.Sprintf("DROP TABLE IF EXISTS %s", qualified))
	})
}
