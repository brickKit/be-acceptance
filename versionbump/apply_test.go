package versionbump

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile 是测试夹具用的小工具：建目录 + 写文件。
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const inventoryYAML = `apiVersion: brickkit/v1
kind: Component

metadata:
  id: erp/inventory                  # ⚠️ Fork 件必须与标准件完全一致（§3.4.1）
  name: 库存管理
  version: 1.0.12                    # 精确版本，不接受 ^ / ~ / latest
                                      # v1.0.12：上一条历史记录，保持原样不动

dependencies:
  components: []

deployment:
  type: container
  image: brickenterprise/erp-inventory:1.0.12
`

const salesYAML = `apiVersion: brickkit/v1
kind: Component

metadata:
  id: erp/sales
  name: 销售管理
  version: 1.0.17                    # 精确版本，不接受 ^ / ~ / latest

dependencies:
  components:
    - erp/inventory@1.0.12
    - erp/finance@1.0.8

deployment:
  type: container
  image: brickenterprise/erp-sales:1.0.17
`

const financeYAML = `apiVersion: brickkit/v1
kind: Component

metadata:
  id: erp/finance
  name: 财务管理
  version: 1.0.8                     # 精确版本，不接受 ^ / ~ / latest

dependencies:
  components: []

deployment:
  type: container
  image: brickenterprise/erp-finance:1.0.8
`

const brickkitYAMLFixture = `components:
  - id: erp/inventory
    version: 1.0.12 # v1.0.12：上一条历史记录
    expose: true
  - id: erp/sales
    version: 1.0.17 # v1.0.17：上一条历史记录
  - id: erp/finance
    version: 1.0.8 # v1.0.8：上一条历史记录
`

const agentsMDFixture = "# roster\n\n" +
	"| Component | Version | Role |\n" +
	"|---|---|---|\n" +
	"| `erp-inventory` | 1.0.12 | Physical command hub |\n" +
	"| `erp-sales` | 1.0.17 | The one link in the chain |\n" +
	"| `erp-finance` | 1.0.8 | Event-sink hub |\n"

func setupFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "components/erp/inventory/component.yaml"), inventoryYAML)
	writeFile(t, filepath.Join(root, "components/erp/sales/component.yaml"), salesYAML)
	writeFile(t, filepath.Join(root, "components/erp/finance/component.yaml"), financeYAML)
	writeFile(t, filepath.Join(root, "brickkit.yaml"), brickkitYAMLFixture)
	writeFile(t, filepath.Join(root, "AGENTS.md"), agentsMDFixture)
	writeFile(t, filepath.Join(root, "docs/zh/AGENTS.md"), agentsMDFixture)
	return root
}

func TestApply_端到端级联落地(t *testing.T) {
	root := setupFixture(t)
	reg, err := LoadRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(reg) != 3 {
		t.Fatalf("期望扫到 3 个组件，实际 %d：%+v", len(reg), reg)
	}

	changes, err := ComputeCascade(reg, []SeedChange{
		{ID: "erp/inventory", Reason: "补属性测试，无行为/契约变更。"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("期望 erp/inventory 自己 + erp/sales 被级联，共 2 条，实际 %d：%+v", len(changes), changes)
	}

	if _, err := Apply(root, reg, changes, false); err != nil {
		t.Fatal(err)
	}

	// --- erp/inventory 自己的 component.yaml ---
	invContent := readFile(t, filepath.Join(root, "components/erp/inventory/component.yaml"))
	if !strings.Contains(invContent, "version: 1.0.13                    # 精确版本，不接受 ^ / ~ / latest") {
		t.Fatalf("version 行的原有说明文字应该原样保留，实际：\n%s", invContent)
	}
	if !strings.Contains(invContent, "# v1.0.13：补属性测试，无行为/契约变更。") {
		t.Fatalf("新的变更记录应该插入进去，实际：\n%s", invContent)
	}
	if !strings.Contains(invContent, "# v1.0.12：上一条历史记录，保持原样不动") {
		t.Fatalf("旧的历史记录应该原样保留，实际：\n%s", invContent)
	}
	if !strings.Contains(invContent, "image: brickenterprise/erp-inventory:1.0.13") {
		t.Fatalf("image tag 应该跟着 version 一起改，实际：\n%s", invContent)
	}

	// --- erp/sales 的 component.yaml：被级联 + 依赖引用要同步 ---
	salesContent := readFile(t, filepath.Join(root, "components/erp/sales/component.yaml"))
	if !strings.Contains(salesContent, "version: 1.0.18") {
		t.Fatalf("erp/sales 应该被级联到 1.0.18，实际：\n%s", salesContent)
	}
	if !strings.Contains(salesContent, "# v1.0.18：依赖版本号同步跟进 erp/inventory@1.0.13，无破坏性变更。") {
		t.Fatalf("erp/sales 应该有自动生成的依赖同步理由，实际：\n%s", salesContent)
	}
	if !strings.Contains(salesContent, "- erp/inventory@1.0.13") {
		t.Fatalf("erp/sales 对 erp/inventory 的依赖引用应该同步到新版本，实际：\n%s", salesContent)
	}
	if !strings.Contains(salesContent, "- erp/finance@1.0.8") {
		t.Fatalf("erp/sales 对 erp/finance 的依赖引用没变，不该被动到，实际：\n%s", salesContent)
	}
	if !strings.Contains(salesContent, "image: brickenterprise/erp-sales:1.0.18") {
		t.Fatalf("erp/sales 的 image tag 应该跟着一起改，实际：\n%s", salesContent)
	}

	// --- erp/finance 完全不该被动 ---
	financeContent := readFile(t, filepath.Join(root, "components/erp/finance/component.yaml"))
	if !strings.Contains(financeContent, "version: 1.0.8 ") {
		t.Fatalf("erp/finance 不依赖 erp/inventory，不该被牵连，实际：\n%s", financeContent)
	}

	// --- brickkit.yaml 顶层 pin ---
	brickkitContent := readFile(t, filepath.Join(root, "brickkit.yaml"))
	if !strings.Contains(brickkitContent, "version: 1.0.13 # v1.0.13：补属性测试，无行为/契约变更。") {
		t.Fatalf("brickkit.yaml 里 erp/inventory 的 pin 应该更新，实际：\n%s", brickkitContent)
	}
	if !strings.Contains(brickkitContent, "# v1.0.12：上一条历史记录") {
		t.Fatalf("brickkit.yaml 里 erp/inventory 的旧记录应该被推到下一行变成纯注释，实际：\n%s", brickkitContent)
	}
	if !strings.Contains(brickkitContent, "version: 1.0.18 # v1.0.18：依赖版本号同步跟进 erp/inventory@1.0.13，无破坏性变更。") {
		t.Fatalf("brickkit.yaml 里 erp/sales 的 pin 应该更新，实际：\n%s", brickkitContent)
	}
	if !strings.Contains(brickkitContent, "version: 1.0.8 # v1.0.8：上一条历史记录") {
		t.Fatalf("brickkit.yaml 里 erp/finance 的 pin 不该被动，实际：\n%s", brickkitContent)
	}

	// --- 两份 AGENTS.md 名录表 ---
	for _, p := range []string{"AGENTS.md", "docs/zh/AGENTS.md"} {
		content := readFile(t, filepath.Join(root, p))
		if !strings.Contains(content, "| `erp-inventory` | 1.0.13 |") {
			t.Fatalf("%s 里 erp-inventory 的版本号应该更新，实际：\n%s", p, content)
		}
		if !strings.Contains(content, "| `erp-sales` | 1.0.18 |") {
			t.Fatalf("%s 里 erp-sales 的版本号应该更新，实际：\n%s", p, content)
		}
		if !strings.Contains(content, "| `erp-finance` | 1.0.8 |") {
			t.Fatalf("%s 里 erp-finance 不该被动，实际：\n%s", p, content)
		}
	}
}

func TestApply_dryRun不写文件(t *testing.T) {
	root := setupFixture(t)
	reg, err := LoadRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := ComputeCascade(reg, []SeedChange{{ID: "erp/inventory", Reason: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	before := readFile(t, filepath.Join(root, "components/erp/inventory/component.yaml"))
	if _, err := Apply(root, reg, changes, true); err != nil {
		t.Fatal(err)
	}
	after := readFile(t, filepath.Join(root, "components/erp/inventory/component.yaml"))
	if before != after {
		t.Fatal("dryRun=true 不该真的写文件")
	}
}

func TestApply_文件已被改动过时拒绝盲写(t *testing.T) {
	root := setupFixture(t)
	reg, err := LoadRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := ComputeCascade(reg, []SeedChange{{ID: "erp/inventory", Reason: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	// 模拟"算完级联之后、真正落地之前，文件又被别的改动碰过"。
	writeFile(t, filepath.Join(root, "components/erp/inventory/component.yaml"),
		strings.Replace(inventoryYAML, "1.0.12", "1.0.99", 1))
	if _, err := Apply(root, reg, changes, false); err == nil {
		t.Fatal("期望报错拒绝盲写，实际没有报错")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSyncHostnameLiteral_只改指定配置键的字面量(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "components/infra/authz/component.yaml"), `metadata:
  id: infra/authz
  version: 1.0.4
`)
	writeFile(t, filepath.Join(root, "components/crm/opportunity/component.yaml"), `configSchema:
  properties:
    authzBundleUrl:
      type: string
    iamJwksUrl:
      type: string
`)
	writeFile(t, filepath.Join(root, "brickkit.yaml"), `components:
  - id: crm/opportunity
    config:
      authzBundleUrl: "http://infra-authz-1-0-4:8223/authz/bundle"
      iamJwksUrl: "http://infra-iam-casdoor-1-0-6:8200/.well-known/jwks.json"
`)

	touched, err := syncHostnameLiteral(root, "authzBundleUrl", "infra-authz-1-0-4", "infra-authz-1-0-5", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(touched) != 1 {
		t.Fatalf("期望只有 brickkit.yaml 被改（component.yaml 里的是 schema 声明不是字面量），实际 touched=%v", touched)
	}

	content := readFile(t, filepath.Join(root, "brickkit.yaml"))
	if !strings.Contains(content, `authzBundleUrl: "http://infra-authz-1-0-5:8223/authz/bundle"`) {
		t.Fatalf("authzBundleUrl 的主机名应该同步，实际：\n%s", content)
	}
	if !strings.Contains(content, `iamJwksUrl: "http://infra-iam-casdoor-1-0-6:8200/.well-known/jwks.json"`) {
		t.Fatalf("iamJwksUrl 不该被这次 authzBundleUrl 的同步动到，实际：\n%s", content)
	}
}
