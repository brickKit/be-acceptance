package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brickKit/be-acceptance/versionbump"
)

func mkComp(t *testing.T, root, rel, id string, goMod bool) *versionbump.Component {
	t.Helper()
	dir := filepath.Join(root, rel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if goMod {
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &versionbump.Component{ID: id, Dir: dir, Version: "2.0.0"}
}

func mustContain(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("输出应包含 %q，实际：\n%s", w, out)
		}
	}
}

func mustNotContain(t *testing.T, out string, bads ...string) {
	t.Helper()
	for _, b := range bads {
		if strings.Contains(out, b) {
			t.Errorf("输出不应包含 %q，实际：\n%s", b, out)
		}
	}
}

// v0.4 时代的残留：make image、docker push、只打 v 前缀 tag——v1 规则下全部是错的。
var v04Leftovers = []string{"make image", "docker push", "-m \"v"}

func TestNextSteps_Go组件(t *testing.T) {
	root := t.TempDir()
	c := mkComp(t, root, "components/mdm/customer", "mdm/customer", true)
	out := strings.Join(nextSteps(root, c, "2.0.1", []string{"component.yaml"}), "\n")
	mustContain(t, out,
		"components/mdm/customer",
		"git commit -F",
		"git push origin main",
		"brickkit release --notes-file <发布说明文件>",
		"tag 2.0.1（不带 v）",
		"git tag -a v2.0.1 -F <发布说明文件>",
		"git push origin v2.0.1",
		"brickkit build mdm/customer",
	)
	mustNotContain(t, out, v04Leftovers...)
	// release 必须先于 v tag：v tag 打在 release 那个提交上
	if strings.Index(out, "brickkit release") > strings.Index(out, "git tag -a v2.0.1") {
		t.Errorf("brickkit release 应在 v 前缀 tag 之前：\n%s", out)
	}
}

func TestNextSteps_Python或TS组件只打一个tag(t *testing.T) {
	root := t.TempDir()
	c := mkComp(t, root, "components/infra/print", "infra/print", false)
	out := strings.Join(nextSteps(root, c, "2.0.1", []string{"component.yaml"}), "\n")
	mustContain(t, out,
		"brickkit release --notes-file <发布说明文件>",
		"tag 2.0.1（不带 v）",
		"brickkit build infra/print",
	)
	mustNotContain(t, out, append(v04Leftovers, "git tag -a v", "git push origin v2.0.1")...)
}

func TestNextSteps_外壳在装配仓库根用path发布且不打裸v标签(t *testing.T) {
	root := t.TempDir()
	c := mkComp(t, root, "shell/be/go-core", "be/go-core", true) // 外壳也是 Go，但不打 v tag
	out := strings.Join(nextSteps(root, c, "1.0.1", []string{"component.yaml", "go.mod"}), "\n")
	mustContain(t, out,
		"装配仓库根目录",
		"git add shell/be/go-core/component.yaml shell/be/go-core/go.mod",
		"brickkit release --path shell/be/go-core --notes-file <发布说明文件>",
		"tag be-go-core/1.0.1",
		"不要在装配仓库打裸 v 标签",
		"brickkit build be/go-core",
	)
	mustNotContain(t, out, append(v04Leftovers, "git tag -a v", "git push origin v1.0.1")...)
}

func TestBumpVersionFooter不再指向已退役的总纲(t *testing.T) {
	mustNotContain(t, applyFooter, "00-master-guide", "SOP-W")
	mustContain(t, applyFooter, ".claude/skills/version-bump-ship/", "docs/conventions/development-workflow.md")
}

// 成员升版本时外壳 go.mod 的改动记在成员那条结果里；外壳的 git add 也必须带上它。
func TestFilesUnder_按目录收集且去重(t *testing.T) {
	root := t.TempDir()
	shell := filepath.Join(root, "shell/be/go-core")
	member := filepath.Join(root, "components/mdm/customer")
	all := []string{
		filepath.Join(member, "component.yaml"),
		filepath.Join(shell, "go.mod"),
		filepath.Join(shell, "component.yaml"),
		filepath.Join(shell, "go.mod"),
		filepath.Join(root, "shell/be/go-core-x/component.yaml"), // 同前缀的另一个目录不算
	}
	got := strings.Join(filesUnder(shell, all), ",")
	if got != "go.mod,component.yaml" {
		t.Fatalf("期望 go.mod,component.yaml，得到 %s", got)
	}
}
