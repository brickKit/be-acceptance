package infra

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteEnvFileKeepsValuesVerbatim(t *testing.T) {
	p := filepath.Join(t.TempDir(), "env")
	if err := writeEnvFile(p, []string{`A={"mode":"on"}`, "B=x y", "C="}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "A={\"mode\":\"on\"}\nB=x y\nC=\n" {
		t.Fatalf("env file = %q", b)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", st.Mode())
	}
	if err := writeEnvFile(p, []string{"A=line1\nline2"}); err == nil {
		t.Fatal("multi-line value accepted: docker env files cannot carry it")
	}
}
