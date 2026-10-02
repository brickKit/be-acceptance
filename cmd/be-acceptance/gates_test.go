package main

import (
	"errors"
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

// --only：只扫一个组件，--strict 只作用于它；未知组件是用法错误（exit 2）。
func TestRunGate_only(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, "components/erp/sales/component.yaml", "1.0.26", "pgSchema")
	writeManifest(t, root, "components/mdm/customer/component.yaml", "2.0.0", "PG_HOST")
	if err := runGate([]string{"config-key-scan", "--root", root, "--strict", "--only", "components/mdm/customer"}); err != nil {
		t.Fatalf("--only mdm/customer --strict：别的 1.x 组件不该判红：%v", err)
	}
	if err := runGate([]string{"config-key-scan", "--root", root, "--strict", "--only", "components/erp/sales"}); err == nil {
		t.Fatal("--only erp/sales --strict 要判红")
	}
	if err := runGate([]string{"openapi-additive-scan", "--root", root, "--only", "components/mdm/customer"}); err != nil {
		t.Fatalf("openapi --only：%v", err)
	}
	for _, args := range [][]string{
		{"config-key-scan", "--root", root, "--only", "mdm/nope"},
		{"openapi-additive-scan", "--root", root, "--only", "mdm/nope"},
		{"import-scan", "--root", root, "--only", "components/mdm/customer"},
	} {
		err := runGate(args)
		if err == nil || exitCode(err) != 2 {
			t.Fatalf("%v：应是用法错误（exit 2），得到 %v", args, err)
		}
	}
	if exitCode(errors.New("x")) != 1 {
		t.Fatal("普通错误 exit 1")
	}
}

// --only 时 ⚠（本该对比却没能对比，如组件目录不是独立仓库）判红：发布前"没比"不能算通过。
func TestRunGate_only_没能对比判红(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, "components/mdm/customer/component.yaml", "2.0.0", "PG_HOST")
	spec := filepath.Join(root, "components/mdm/customer/contracts/customer.openapi.yaml")
	if err := os.MkdirAll(filepath.Dir(spec), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spec, []byte("openapi: 3.0.3\npaths: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runGate([]string{"openapi-additive-scan", "--root", root, "--only", "mdm/customer"})
	if err == nil || exitCode(err) != 1 {
		t.Fatalf("不是独立仓库、没能对比：--only 下要判红（exit 1），得到 %v", err)
	}
	if err := runGate([]string{"openapi-additive-scan", "--root", root}); err != nil {
		t.Fatalf("不带 --only 时 ⚠ 只警告：%v", err)
	}
}
