// Group F · 生命周期（用例 18）——`remove` 连 `.archived/` 一起删。
//
// ⚠️ `components/` 在真实装配仓库里全是登记过的 git submodule，
// brickKit v0.2.1 起 `sync`/`remove` 撞见已登记的 submodule 会报
// `SUBMODULE_GUARD` 并打印手工命令，不会自动帮你搬/删——这是预期行为
// （见根目录 AGENTS.md「三条运维禁令」），不是这条断言要验的东西。
// 用例 18 要验的是"remove 连 .archived/ 一起删"这个语义本身，所以用一个
// 临时的、普通 git 仓库（不是 submodule）里的假组件验，不拿真组件试。
package platform_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestPlatform18_remove连archived一起删(t *testing.T) {
	requireCommand(t, "git")
	f := newFixture(t)
	f.writeFile("brickkit.yaml", `project: f18
deploy:
  target: docker
sources:
  - id: local-dev
    type: local
    path: ./components
components:
  - id: foo/bar
    version: 1.0.0
    enabled: false
`)
	f.writeFile("components/foo/bar/component.yaml", minimalComponent("foo/bar", "1.0.0", 19001))

	// remove 对"删了找不回来"有安全网（不是 git 仓库/有未提交改动/有
	// 没推送的提交都会拦下来）——这条断言要验的是"归档目录会不会被
	// 一起删"这个语义本身，不是安全网机制，所以走真实 git 仓库 + 真实
	// commit + --force 跳过安全网，而不是费劲搭一个远端。
	gitCmd := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = f.dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s 失败：%v\n%s", strings.Join(args, " "), err, out)
		}
	}
	gitCmd("init", "-q")
	gitCmd("config", "user.email", "test@example.com")
	gitCmd("config", "user.name", "test")
	gitCmd("add", "-A")
	gitCmd("commit", "-q", "-m", "init")

	// 第一步：sync——foo/bar 是 enabled: false，判据与 up 一致，不会
	// 被启动，该被归档。
	syncRes := f.run("sync")
	if syncRes.exitCode != 0 {
		t.Fatalf("期望 sync 成功，实际退出码 %d：%s", syncRes.exitCode, syncRes.stdout)
	}
	if !fileExists(f.dir + "/components/.archived/foo/bar/component.yaml") {
		t.Fatalf("期望 sync 后 foo/bar 被归档到 components/.archived/foo/bar，实际没有找到")
	}
	if fileExists(f.dir + "/components/foo/bar/component.yaml") {
		t.Fatalf("期望 sync 后活跃目录 components/foo/bar 不再存在")
	}

	// 第二步：remove --force——归档目录也该被删干净，不是只从
	// brickkit.yaml 摘掉条目了事。
	removeRes := f.run("remove", "foo/bar@1.0.0", "--force")
	if removeRes.exitCode != 0 {
		t.Fatalf("期望 remove 成功，实际退出码 %d：%s", removeRes.exitCode, removeRes.stdout)
	}
	if !strings.Contains(removeRes.stdout, "已删除归档源码目录") {
		t.Fatalf("期望输出确认删除了归档源码目录，实际：%s", removeRes.stdout)
	}
	if fileExists(f.dir + "/components/.archived/foo/bar/component.yaml") {
		t.Fatalf("期望 remove 之后 components/.archived/foo/bar 也被删除，实际还在")
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
