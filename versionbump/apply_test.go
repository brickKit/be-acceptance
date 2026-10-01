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
  version: 2.0.12                    # 精确版本，不接受 ^ / ~ / latest
                                      # v2.0.12：上一条历史记录，保持原样不动

dependencies:
  components: []

deployment:
  type: container
  image: brickenterprise/erp-inventory:2.0.12
`

const salesYAML = `apiVersion: brickkit/v1
kind: Component

metadata:
  id: erp/sales
  name: 销售管理
  version: 2.0.17                    # 精确版本，不接受 ^ / ~ / latest

dependencies:
  components:
    - erp/inventory@2.0.12
    - erp/finance@2.0.8

deployment:
  type: container
  image: brickenterprise/erp-sales:2.0.17
`

const financeYAML = `apiVersion: brickkit/v1
kind: Component

metadata:
  id: erp/finance
  name: 财务管理
  version: 2.0.8                     # 精确版本，不接受 ^ / ~ / latest

dependencies:
  components: []

deployment:
  type: container
  image: brickenterprise/erp-finance:2.0.8
`

// v1 布局：外壳在 shell/be/<name>/，成员写在 shell.members，go.mod 的
// require 对 v2+ 带 /vN 模块路径后缀。
const shellYAML = `apiVersion: brickkit/v1
kind: Component

metadata:
  id: be/go-core
  name: go-core 外壳
  version: 0.5.6                     # 精确版本，不接受 ^ / ~ / latest

shell:
  members:
    - erp/inventory@2.0.12
    - erp/finance@2.0.8

deployment:
  type: container
  image: brickenterprise/be-shell-go:0.5.6
`

const shellGoMod = `module github.com/brickKit/be-shell-go

go 1.25.11

require (
	github.com/brickKit/be-sdk-go v0.2.7
	github.com/brickKit/erp-inventory/v2 v2.0.12
	github.com/brickKit/erp-inventory/gen/erp/inventory v1.0.3
	github.com/brickKit/erp-finance/v2 v2.0.8
)
`

func setupFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "components/erp/inventory/component.yaml"), inventoryYAML)
	writeFile(t, filepath.Join(root, "components/erp/sales/component.yaml"), salesYAML)
	writeFile(t, filepath.Join(root, "components/erp/finance/component.yaml"), financeYAML)
	writeFile(t, filepath.Join(root, "shell/be/go-core/component.yaml"), shellYAML)
	writeFile(t, filepath.Join(root, "shell/be/go-core/go.mod"), shellGoMod)
	return root
}

func TestApply_端到端级联落地(t *testing.T) {
	root := setupFixture(t)
	reg, err := LoadRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(reg) != 4 {
		t.Fatalf("期望扫到 3 个组件 + 1 个外壳共 4 个，实际 %d：%+v", len(reg), reg)
	}

	changes, err := ComputeCascade(reg, []SeedChange{
		{ID: "erp/inventory", Reason: "补属性测试，无行为/契约变更。"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 3 {
		t.Fatalf("期望 erp/inventory 自己 + erp/sales + 外壳 be/go-core 被级联，共 3 条，实际 %d：%+v", len(changes), changes)
	}

	if _, err := Apply(root, reg, changes, false); err != nil {
		t.Fatal(err)
	}

	// --- erp/inventory 自己的 component.yaml ---
	invContent := readFile(t, filepath.Join(root, "components/erp/inventory/component.yaml"))
	if !strings.Contains(invContent, "version: 2.0.13                    # 精确版本，不接受 ^ / ~ / latest") {
		t.Fatalf("version 行的原有说明文字应该原样保留，实际：\n%s", invContent)
	}
	if !strings.Contains(invContent, "# v2.0.13：补属性测试，无行为/契约变更。") {
		t.Fatalf("新的变更记录应该插入进去，实际：\n%s", invContent)
	}
	if !strings.Contains(invContent, "# v2.0.12：上一条历史记录，保持原样不动") {
		t.Fatalf("旧的历史记录应该原样保留，实际：\n%s", invContent)
	}
	if !strings.Contains(invContent, "image: brickenterprise/erp-inventory:2.0.13") {
		t.Fatalf("image tag 应该跟着 version 一起改，实际：\n%s", invContent)
	}

	// --- erp/sales：被级联 + 依赖引用要同步 ---
	salesContent := readFile(t, filepath.Join(root, "components/erp/sales/component.yaml"))
	if !strings.Contains(salesContent, "version: 2.0.18") {
		t.Fatalf("erp/sales 应该被级联到 2.0.18，实际：\n%s", salesContent)
	}
	if !strings.Contains(salesContent, "# v2.0.18：依赖版本号同步跟进 erp/inventory@2.0.13，无破坏性变更。") {
		t.Fatalf("erp/sales 应该有自动生成的依赖同步理由，实际：\n%s", salesContent)
	}
	if !strings.Contains(salesContent, "- erp/inventory@2.0.13") {
		t.Fatalf("erp/sales 对 erp/inventory 的依赖引用应该同步到新版本，实际：\n%s", salesContent)
	}
	if !strings.Contains(salesContent, "- erp/finance@2.0.8") {
		t.Fatalf("erp/sales 对 erp/finance 的依赖引用没变，不该被动到，实际：\n%s", salesContent)
	}

	// --- 外壳 component.yaml：shell.members 同步 + 自己被级联升版本 ---
	shellContent := readFile(t, filepath.Join(root, "shell/be/go-core/component.yaml"))
	if !strings.Contains(shellContent, "- erp/inventory@2.0.13") {
		t.Fatalf("外壳 shell.members 里的 erp/inventory 应该同步到 2.0.13，实际：\n%s", shellContent)
	}
	if !strings.Contains(shellContent, "- erp/finance@2.0.8") {
		t.Fatalf("外壳 shell.members 里的 erp/finance 没变，不该被动，实际：\n%s", shellContent)
	}
	if !strings.Contains(shellContent, "version: 0.5.7") || !strings.Contains(shellContent, "image: brickenterprise/be-shell-go:0.5.7") {
		t.Fatalf("外壳自己应该被级联到 0.5.7（version 与 image tag 一起），实际：\n%s", shellContent)
	}

	// --- 外壳 go.mod：带 /v2 后缀的 require 同步，gen 子模块不动 ---
	goMod := readFile(t, filepath.Join(root, "shell/be/go-core/go.mod"))
	if !strings.Contains(goMod, "github.com/brickKit/erp-inventory/v2 v2.0.13\n") {
		t.Fatalf("go.mod 里 erp-inventory/v2 应该同步到 v2.0.13，实际：\n%s", goMod)
	}
	if !strings.Contains(goMod, "github.com/brickKit/erp-inventory/gen/erp/inventory v1.0.3\n") {
		t.Fatalf("go.mod 里 gen 子模块不该被动，实际：\n%s", goMod)
	}
	if !strings.Contains(goMod, "github.com/brickKit/erp-finance/v2 v2.0.8\n") {
		t.Fatalf("go.mod 里 erp-finance/v2 没变，不该被动，实际：\n%s", goMod)
	}

	// --- erp/finance 完全不该被动 ---
	financeContent := readFile(t, filepath.Join(root, "components/erp/finance/component.yaml"))
	if !strings.Contains(financeContent, "version: 2.0.8 ") {
		t.Fatalf("erp/finance 不依赖 erp/inventory，不该被牵连，实际：\n%s", financeContent)
	}
}

// bump-version 不再碰 brickkit.yaml 顶层 pin（`brickkit upgrade`）、不再碰
// config: 主机名字面量（`$var:`）、不再碰 AGENTS 名册（CLI 维护块）。
func TestApply_不再改brickkit点yaml和AGENTS名册(t *testing.T) {
	root := setupFixture(t)
	const bk = "components:\n  - id: erp/inventory\n    version: 2.0.12\n"
	const agents = "| `erp-inventory` | 2.0.12 |\n"
	writeFile(t, filepath.Join(root, "brickkit.yaml"), bk)
	writeFile(t, filepath.Join(root, "AGENTS.md"), agents)
	writeFile(t, filepath.Join(root, "AGENTS.zh.md"), agents)

	reg, err := LoadRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := ComputeCascade(reg, []SeedChange{{ID: "erp/inventory", Reason: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(root, reg, changes, false); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(root, "brickkit.yaml")); got != bk {
		t.Fatalf("brickkit.yaml 不该被改，实际：\n%s", got)
	}
	for _, p := range []string{"AGENTS.md", "AGENTS.zh.md"} {
		if got := readFile(t, filepath.Join(root, p)); got != agents {
			t.Fatalf("%s 不该被改，实际：\n%s", p, got)
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
	paths := []string{
		"components/erp/inventory/component.yaml",
		"shell/be/go-core/component.yaml",
		"shell/be/go-core/go.mod",
	}
	before := map[string]string{}
	for _, p := range paths {
		before[p] = readFile(t, filepath.Join(root, p))
	}
	if _, err := Apply(root, reg, changes, true); err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		if readFile(t, filepath.Join(root, p)) != before[p] {
			t.Fatalf("dryRun=true 不该真的写文件：%s", p)
		}
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
		strings.Replace(inventoryYAML, "2.0.12", "2.0.99", 1))
	if _, err := Apply(root, reg, changes, false); err == nil {
		t.Fatal("期望报错拒绝盲写，实际没有报错")
	}
}

// 显式跨大版本（NewVer 的 major 变了）时，go.mod 的 /vN 路径后缀要跟着变。
func TestApply_跨大版本时go点mod的模块路径后缀跟着变(t *testing.T) {
	root := setupFixture(t)
	reg, err := LoadRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := ComputeCascade(reg, []SeedChange{{ID: "erp/inventory", Reason: "破坏性变更", NewVer: "3.0.0"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(root, reg, changes, false); err != nil {
		t.Fatal(err)
	}
	goMod := readFile(t, filepath.Join(root, "shell/be/go-core/go.mod"))
	if !strings.Contains(goMod, "github.com/brickKit/erp-inventory/v3 v3.0.0\n") {
		t.Fatalf("跨到 3.0.0 时 go.mod 应该写成 /v3 v3.0.0，实际：\n%s", goMod)
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
