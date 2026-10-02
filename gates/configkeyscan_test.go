package gates

import (
	"path/filepath"
	"testing"
)

func manifestWithKeys(id, version string, keys ...string) string {
	s := "apiVersion: brickkit/v1\n" +
		"kind: Component\n\n" +
		"metadata:\n" +
		"  id: " + id + "\n" +
		"  version: " + version + "   # 精确版本\n\n"
	if len(keys) == 0 {
		return s
	}
	s += "configSchema:\n  type: object\n  properties:\n"
	for _, k := range keys {
		s += "    " + k + ":\n      type: string\n"
	}
	s += "  required: []\n"
	return s
}

// key+rule 的集合，方便断言"恰好这些"。
func keyRules(vs []ConfigKeyViolation) map[string]bool {
	m := map[string]bool{}
	for _, v := range vs {
		m[v.Key+"|"+v.Rule] = true
	}
	return m
}

func TestConfigKeyScan_合规的键零违规(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/mdm/customer/component.yaml"),
		manifestWithKeys("mdm/customer", "2.0.0", "PG_HOST", "PG_SCHEMA", "NATS_URL", "A", "S3_URL", "X9_Y"))
	write(t, filepath.Join(root, "shell/be/go-core/component.yaml"),
		manifestWithKeys("be/go-core", "1.0.0", "PG_HOST", "IAM_JWKS_URL"))

	vs, err := ConfigKeyScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Fatalf("全部合规，不该有违规：%+v", vs)
	}
}

func TestConfigKeyScan_三条规则各自命中(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/mdm/customer/component.yaml"),
		manifestWithKeys("mdm/customer", "2.0.0",
			"PG_HOST",                        // 合规
			"pgSchema",                       // 驼峰
			"_LEADING",                       // 下划线开头
			"9LIVES",                         // 数字开头
			"PG-HOST",                        // 连字符
			"UPSTREAM_ENDPOINT",              // 平台保留后缀
			"COMPONENT_ID",                   // 保留名
			"COMPONENT_VERSION",              // 保留名
			"PORT",                           // 保留名
			"BRICKKIT_SERVED_MEMBERS",        // 保留名
			"BRICKKIT_SERVED_MEMBERS_CONFIG", // 保留名
			"foo_ENDPOINT",                   // 同时违反两条
		))

	vs, err := ConfigKeyScan(root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"pgSchema|naming":                         true,
		"_LEADING|naming":                         true,
		"9LIVES|naming":                           true,
		"PG-HOST|naming":                          true,
		"UPSTREAM_ENDPOINT|endpoint-suffix":       true,
		"COMPONENT_ID|reserved":                   true,
		"COMPONENT_VERSION|reserved":              true,
		"PORT|reserved":                           true,
		"BRICKKIT_SERVED_MEMBERS|reserved":        true,
		"BRICKKIT_SERVED_MEMBERS_CONFIG|reserved": true,
		"foo_ENDPOINT|naming":                     true,
		"foo_ENDPOINT|endpoint-suffix":            true,
	}
	got := keyRules(vs)
	if len(got) != len(want) {
		t.Fatalf("期望 %d 条，得到 %d：%+v", len(want), len(got), vs)
	}
	for k := range want {
		if !got[k] {
			t.Errorf("漏报 %s，全部=%+v", k, vs)
		}
	}
	for _, v := range vs {
		if v.File != "components/mdm/customer/component.yaml" {
			t.Errorf("File 应是相对根的路径，得到 %q", v.File)
		}
		if v.Line == 0 {
			t.Errorf("%s 没有行号", v.Key)
		}
		if v.Pending {
			t.Errorf("2.x 组件不该标成迁移中：%+v", v)
		}
	}
}

func TestConfigKeyScan_行号指向键所在行(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/mdm/customer/component.yaml"),
		manifestWithKeys("mdm/customer", "2.0.0", "PG_HOST", "pgSchema"))
	vs, _ := ConfigKeyScan(root)
	if len(vs) != 1 {
		t.Fatalf("期望 1 条，得到 %+v", vs)
	}
	// 1 apiVersion 2 kind 3 空 4 metadata 5 id 6 version 7 空 8 configSchema
	// 9 type 10 properties 11 PG_HOST 12 type 13 pgSchema
	if vs[0].Line != 13 {
		t.Fatalf("pgSchema 在第 13 行，得到 %d", vs[0].Line)
	}
}

// 06b 过渡期：还没迁到 2.x 的组件照样报出来（那是事实），但标成 Pending，
// 由 CLI 决定不计入失败；外壳本来就在 1.x，永远严格。
func TestConfigKeyScan_1x组件标为迁移中而外壳不标(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/erp/sales/component.yaml"),
		manifestWithKeys("erp/sales", "1.0.26", "pgSchema"))
	write(t, filepath.Join(root, "components/mdm/customer/component.yaml"),
		manifestWithKeys("mdm/customer", "2.0.0", "pgSchema"))
	write(t, filepath.Join(root, "shell/be/go-core/component.yaml"),
		manifestWithKeys("be/go-core", "1.0.0", "pgSchema"))

	vs, err := ConfigKeyScan(root)
	if err != nil {
		t.Fatal(err)
	}
	pending := map[string]bool{}
	for _, v := range vs {
		pending[v.File] = v.Pending
	}
	if len(pending) != 3 {
		t.Fatalf("三个清单都该报出来，得到 %+v", vs)
	}
	if !pending["components/erp/sales/component.yaml"] {
		t.Error("1.0.26 的组件应标为迁移中")
	}
	if pending["components/mdm/customer/component.yaml"] {
		t.Error("2.0.0 的组件不该标为迁移中")
	}
	if pending["shell/be/go-core/component.yaml"] {
		t.Error("外壳 1.x 是常态，不该标为迁移中")
	}
}

func TestConfigKeyScan_没有configSchema或清单的目录跳过(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/frontend/standard/component.yaml"),
		manifestWithKeys("frontend/standard", "2.0.0"))
	write(t, filepath.Join(root, "components/mdm/empty/README.md"), "没有 component.yaml")
	vs, err := ConfigKeyScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Fatalf("没有配置项不该报：%+v", vs)
	}
}

func TestConfigKeyScan_清单不是合法YAML报错(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/mdm/customer/component.yaml"), "metadata: [\n")
	if _, err := ConfigKeyScan(root); err == nil {
		t.Fatal("坏 YAML 应该报错，而不是静默通过")
	}
}

// 审查 Minor 4：撞保留名 / _ENDPOINT 后缀与版本无关，现在就是线上 bug（平台的值获胜），
// 1.x 组件也不宽松；过渡期只放宽 naming 一条。
func TestConfigKeyScan_1x组件只有naming宽松(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "components/erp/sales/component.yaml"),
		manifestWithKeys("erp/sales", "1.0.26", "pgSchema", "PORT", "UPSTREAM_ENDPOINT"))
	vs, err := ConfigKeyScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 3 {
		t.Fatalf("期望 3 条，得到 %+v", vs)
	}
	for _, v := range vs {
		if want := v.Rule == "naming"; v.Pending != want {
			t.Errorf("%s [%s]：Pending 应为 %v", v.Key, v.Rule, want)
		}
	}
}
