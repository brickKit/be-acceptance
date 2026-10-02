package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeManifest(t *testing.T, root, rel, version, key string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	s := "metadata:\n  id: x/y\n  version: " + version + "\nconfigSchema:\n  properties:\n    " + key + ":\n      type: string\n"
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 过渡期：1.x 组件的违规只警告，--strict 判红；2.x 组件与外壳的违规始终判红。
func TestRunConfigKeyScan_过渡期与严格模式(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, "components/erp/sales/component.yaml", "1.0.26", "pgSchema")
	if err := runConfigKeyScan(root, false); err != nil {
		t.Fatalf("只有 1.x 组件违规时不该判红：%v", err)
	}
	if err := runConfigKeyScan(root, true); err == nil {
		t.Fatal("--strict 下 1.x 组件违规也要判红")
	}

	writeManifest(t, root, "components/mdm/customer/component.yaml", "2.0.0", "PG_ENDPOINT")
	if err := runConfigKeyScan(root, false); err == nil {
		t.Fatal("2.x 组件违规要判红")
	}

	root2 := t.TempDir()
	writeManifest(t, root2, "shell/be/go-core/component.yaml", "1.0.0", "PORT")
	if err := runConfigKeyScan(root2, false); err == nil {
		t.Fatal("外壳违规要判红（外壳在 1.x 是常态，不享受过渡期）")
	}
}
