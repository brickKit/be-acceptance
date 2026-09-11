package gates

import (
	"path/filepath"
	"testing"
)

func TestHasDataScopeSignal_ErrForbidden算数(t *testing.T) {
	if !hasDataScopeSignal("TestGetTaskDetail_范围外ErrForbidden") {
		t.Fatal("带 ErrForbidden 的测试名应该算数")
	}
}

func TestHasDataScopeSignal_授权加拒绝的组合算数(t *testing.T) {
	if !hasDataScopeSignal("TestWarehouseAccess_授权不存在的仓库拒绝") {
		t.Fatal("授权+拒绝的组合应该算数")
	}
}

// ⚠️ 实测踩坑：第一版信号词表只有单独的"拒绝"，会把 crm-opportunity 的
// TestCreateOpportunity_真实客户不存在时拒绝（纯粹是"客户不存在"这种
// 校验错误，跟数据权限越权毫无关系）误判成"已经测过边界"。改成"拒绝"
// 必须跟"授权/范围/越权"这类限定词成对出现才算数。
func TestHasDataScopeSignal_孤立的拒绝不算数(t *testing.T) {
	if hasDataScopeSignal("TestCreateOpportunity_真实客户不存在时拒绝") {
		t.Fatal("单纯的校验错误（客户不存在）不该被当成数据权限边界测试")
	}
}

func TestHasDataScopeSignal_范围内可以看到本身不算数(t *testing.T) {
	// "范围内可以看到"只验证了正向路径（能看到自己范围内的数据），没有
	// 验证越权会被拒绝——这条本身不该被当成"边界已测过"的证据。
	if hasDataScopeSignal("TestGetOrder_范围内可以看到") {
		t.Fatal("只验证正向路径的测试不该被当成边界测试的证据")
	}
}

func TestDataScopeDimensions_none返回空(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "component.yaml"), "id: mdm/customer\n")
	write(t, filepath.Join(root, "assembly.yaml"), "data_scopes: none # 全员可见\n\npermissions:\n  - { key: x }\n")
	dims, err := dataScopeDimensions(filepath.Join(root, "assembly.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(dims) != 0 {
		t.Fatalf("data_scopes: none 应该返回空维度集合，得到 %v", dims)
	}
}

func TestDataScopeDimensions_多维度去重且跨多行(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "assembly.yaml"), `data_scopes:
  - { dimension: org,   column: dept_path, mode: prefix, tables: [sales_orders] }
  - { dimension: owner, column: owner_id,  mode: equals, tables: [sales_orders] }
  - { dimension: org,   column: dept_path, mode: prefix, tables: [sales_order_items] }

permissions:
  - { key: erp.sales.view }
`)
	dims, err := dataScopeDimensions(filepath.Join(root, "assembly.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(dims) != 2 {
		t.Fatalf("org 出现两次应该去重成一条，期望 [org owner]，得到 %v", dims)
	}
}

func TestDataScopeDimensions_跨行entry也能解析(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "assembly.yaml"), `data_scopes:
  - { dimension: legal_entity, column: legal_entity_id, mode: in,
      tables: [finance_journal_entries, ar_ledger] }

permissions:
  - { key: erp.finance.view }
`)
	dims, err := dataScopeDimensions(filepath.Join(root, "assembly.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(dims) != 1 || dims[0] != "legal_entity" {
		t.Fatalf("跨行的 entry 也该解析出 legal_entity，得到 %v", dims)
	}
}

func TestDataScopeTestScan_有边界测试的组件不报违规(t *testing.T) {
	root := t.TempDir()
	compDir := filepath.Join(root, "components/erp/sales")
	write(t, filepath.Join(compDir, "assembly.yaml"), `data_scopes:
  - { dimension: org,   column: dept_path, mode: prefix, tables: [sales_orders] }
  - { dimension: owner, column: owner_id,  mode: equals, tables: [sales_orders] }

permissions:
  - { key: erp.sales.view }
`)
	write(t, filepath.Join(compDir, "backend/internal/service/service_test.go"), `package service

import "testing"

func TestGetOrder_范围外ErrForbidden(t *testing.T) {}
`)
	gaps, err := DataScopeTestScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 0 {
		t.Fatalf("已经有 ErrForbidden 测试，不该报违规：%+v", gaps)
	}
}

func TestDataScopeTestScan_零覆盖的组件报违规(t *testing.T) {
	root := t.TempDir()
	compDir := filepath.Join(root, "components/crm/opportunity")
	write(t, filepath.Join(compDir, "assembly.yaml"), `data_scopes:
  - { dimension: org,   column: dept_path, mode: prefix, tables: [opportunities] }
  - { dimension: owner, column: owner_id,  mode: equals, tables: [opportunities] }

permissions:
  - { key: crm.opportunity.view }
`)
	write(t, filepath.Join(compDir, "backend/internal/service/service_test.go"), `package service

import "testing"

func TestCreateOpportunity_真实客户不存在时拒绝(t *testing.T) {}
`)
	gaps, err := DataScopeTestScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 1 || gaps[0].Component != "crm/opportunity" {
		t.Fatalf("应该报 crm/opportunity 缺失，得到 %+v", gaps)
	}
}

func TestDataScopeTestScan_data_scopes为none的组件跳过(t *testing.T) {
	root := t.TempDir()
	compDir := filepath.Join(root, "components/mdm/customer")
	write(t, filepath.Join(compDir, "assembly.yaml"), "data_scopes: none\n\npermissions:\n  - { key: x }\n")
	gaps, err := DataScopeTestScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 0 {
		t.Fatalf("data_scopes: none 的组件不该被扫到，得到 %+v", gaps)
	}
}
