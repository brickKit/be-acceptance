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
	if strings.Contains(invContent, "# v2.0.13") || strings.Contains(invContent, "补属性测试") {
		t.Fatalf("component.yaml 不承载历史，不该写入新的变更记录注释，实际：\n%s", invContent)
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
	if strings.Contains(salesContent, "# v2.0.18") {
		t.Fatalf("erp/sales 不该写入变更记录注释，实际：\n%s", salesContent)
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

// v1 -> v2：Go 的语义化导入版本规则要求 v2+ 模块路径带 /v2 后缀，
// 原来没后缀的 require 行要加上。
func TestApply_v1升到v2时go点mod的require加上v2后缀(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "components/erp/inventory/component.yaml"),
		strings.NewReplacer("2.0.12", "1.0.12").Replace(inventoryYAML))
	writeFile(t, filepath.Join(root, "shell/be/go-core/go.mod"),
		"module x\n\nrequire (\n\tgithub.com/brickKit/erp-inventory v1.0.12\n\tgithub.com/brickKit/erp-inventory/gen/erp/inventory v1.0.3\n)\n")
	reg, err := LoadRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := ComputeCascade(reg, []SeedChange{{ID: "erp/inventory", Reason: "大版本", NewVer: "2.0.0"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(root, reg, changes, false); err != nil {
		t.Fatal(err)
	}
	goMod := readFile(t, filepath.Join(root, "shell/be/go-core/go.mod"))
	if !strings.Contains(goMod, "github.com/brickKit/erp-inventory/v2 v2.0.0\n") {
		t.Fatalf("v1→v2 应该加上 /v2 后缀，实际：\n%s", goMod)
	}
	if !strings.Contains(goMod, "github.com/brickKit/erp-inventory/gen/erp/inventory v1.0.3\n") {
		t.Fatalf("gen 子模块不该被动，实际：\n%s", goMod)
	}
}

// v0.x / v1.x 的 patch bump：模块路径不带 /vN，改版本号后也不能凭空加上。
func TestApply_v0和v1的补丁升级不加v后缀(t *testing.T) {
	for _, tc := range []struct{ old, new string }{{"0.3.4", "0.3.5"}, {"1.0.12", "1.0.13"}} {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "components/erp/inventory/component.yaml"),
			strings.NewReplacer("2.0.12", tc.old).Replace(inventoryYAML))
		writeFile(t, filepath.Join(root, "shell/be/go-core/go.mod"),
			"module x\n\nrequire (\n\tgithub.com/brickKit/erp-inventory v"+tc.old+"\n)\n")
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
		goMod := readFile(t, filepath.Join(root, "shell/be/go-core/go.mod"))
		want := "github.com/brickKit/erp-inventory v" + tc.new + "\n"
		if !strings.Contains(goMod, want) || strings.Contains(goMod, "/v0") || strings.Contains(goMod, "/v1 ") {
			t.Fatalf("%s→%s 应该只改版本号、不带后缀，实际：\n%s", tc.old, tc.new, goMod)
		}
	}
}

// 只改 version 行的版本号：不新增任何以 `# v` 开头的行，文件里已有的注释
// （含行尾注释、独立注释行）原样保留；改动前后唯一的差异应当只在版本号、
// image tag 和依赖引用上。
func TestApply_只改版本号不新增也不改动任何注释行(t *testing.T) {
	root := setupFixture(t)
	path := filepath.Join(root, "components/erp/inventory/component.yaml")
	before := readFile(t, path)
	reg, err := LoadRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := ComputeCascade(reg, []SeedChange{{ID: "erp/inventory", Reason: "不该落进文件的理由"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(root, reg, changes, false); err != nil {
		t.Fatal(err)
	}
	after := readFile(t, path)
	want := strings.NewReplacer("2.0.12                    #", "2.0.13                    #", "erp-inventory:2.0.12", "erp-inventory:2.0.13").Replace(before)
	// 历史注释行里的 v2.0.12 不能被上面的替换动到
	want = strings.Replace(want, "# v2.0.13：上一条", "# v2.0.12：上一条", 1)
	if after != want {
		t.Fatalf("除版本号与 image tag 外不该有任何差异。\n期望：\n%s\n实际：\n%s", want, after)
	}
	if strings.Count(after, "\n") != strings.Count(before, "\n") {
		t.Fatal("行数变了，说明插入或删除了行")
	}
}

// v1 允许只写 deployment.build、不写 image：没有 image 行时不报错、不改别的；
// 任意 registry 的 image 只要 tag 等于旧版本就跟着改，tag 与组件版本无关的
// 第三方镜像不碰。
func TestApply_没有image行或image前缀不同也能正确处理(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "components/erp/inventory/component.yaml"), `apiVersion: brickkit/v1
kind: Component

metadata:
  id: erp/inventory
  version: 2.0.12

deployment:
  type: container
  build:
    context: .
`)
	writeFile(t, filepath.Join(root, "components/erp/sales/component.yaml"), `apiVersion: brickkit/v1
kind: Component

metadata:
  id: erp/sales
  version: 2.0.17

dependencies:
  components:
    - erp/inventory@2.0.12

deployment:
  type: container
  image: registry.example.com:5000/acme/erp-sales:2.0.17
`)
	writeFile(t, filepath.Join(root, "components/erp/finance/component.yaml"), `apiVersion: brickkit/v1
kind: Component

metadata:
  id: erp/finance
  version: 2.0.8

deployment:
  type: container
  image: postgres:16.3.1
`)
	reg, err := LoadRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := ComputeCascade(reg, []SeedChange{
		{ID: "erp/inventory", Reason: "x"},
		{ID: "erp/finance", Reason: "y"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(root, reg, changes, false); err != nil {
		t.Fatal(err)
	}
	inv := readFile(t, filepath.Join(root, "components/erp/inventory/component.yaml"))
	if !strings.Contains(inv, "version: 2.0.13") || strings.Contains(inv, "image:") {
		t.Fatalf("没有 image 行时只改 version，实际：\n%s", inv)
	}
	sales := readFile(t, filepath.Join(root, "components/erp/sales/component.yaml"))
	if !strings.Contains(sales, "image: registry.example.com:5000/acme/erp-sales:2.0.18") {
		t.Fatalf("任意 registry 的 image tag 应该跟着改，实际：\n%s", sales)
	}
	fin := readFile(t, filepath.Join(root, "components/erp/finance/component.yaml"))
	if !strings.Contains(fin, "version: 2.0.9") || !strings.Contains(fin, "image: postgres:16.3.1") {
		t.Fatalf("tag 不等于旧版本的第三方镜像不该被改，实际：\n%s", fin)
	}
}
