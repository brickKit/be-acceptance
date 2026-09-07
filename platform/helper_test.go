// Package platform_test 固化设计书 §9.6.2 的 20 条平台断言（Task 19-21）。
// 被测对象是 brickKit 本身，不是我们的业务——平台升级后重跑这批测试
// 就是回归测试。
//
// A/E/D 三组（Task 19）不需要真组件：每条测试自己在 t.TempDir() 里现搭
// 一个最小 brickkit.yaml + 假 component.yaml，对着 brickkit CLI 跑
// --dry-run（或真 up，仅限断言 10 需要真实冷启动计时）。这样写是为了
// 不依赖装配仓库本身的状态——平台断言测的是 brickKit 的行为，不该跟着
// 业务组件的迭代节奏一起变。
//
// B/C/F 三组（Task 20）靠真组件（erp-sales 的依赖树），见 bcf_test.go。
package platform_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// requireCommand 同 closedloop 包的既有判据：需要真实外部命令，不在
// PATH 上就跳过，不是放宽断言。
func requireCommand(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("需要 %s 在 PATH 上：%v", name, err)
	}
	return path
}

// repoRoot 返回装配仓库根目录——固定仓库结构，不是配置项（同 closedloop
// 包 tier0_test.go 的既有判据：go test 的 CWD 是包目录本身，这里是
// tools/be-acceptance/platform/，到仓库根是 3 层 ..）。
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "brickkit.yaml")); err != nil {
		t.Skipf("找不到装配仓库根目录（期望 %s 下有 brickkit.yaml）：%v", root, err)
	}
	return root
}

// fixture 是一次性、隔离的 brickkit 工作区：自己的 brickkit.yaml + 自己
// 的 components/ 目录，跑在 t.TempDir() 里——不碰装配仓库真实的
// brickkit.yaml，平台断言测的是 brickKit 的行为，不该对业务侧产生任何
// 副作用（这批测试全部并发安全、可重复跑）。
type fixture struct {
	t    *testing.T
	dir  string
	bkkt string // brickkit 可执行文件路径
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return &fixture{t: t, dir: t.TempDir(), bkkt: requireCommand(t, "brickkit")}
}

// writeFile 在 fixture 目录下按相对路径写一个文件，自动建父目录。
func (f *fixture) writeFile(relPath, content string) {
	f.t.Helper()
	full := filepath.Join(f.dir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// brickkitResult 是一次 brickkit 命令调用的结果。
type brickkitResult struct {
	stdout   string
	exitCode int
	err      error
}

// run 在 fixture 目录里跑一条 brickkit 命令，不因非零退出码就 Fatal——
// 很多断言恰恰是在验证"这种情况下必须报错"，调用方自己判断 exitCode。
func (f *fixture) run(args ...string) brickkitResult {
	f.t.Helper()
	cmd := exec.Command(f.bkkt, args...)
	cmd.Dir = f.dir
	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			f.t.Fatalf("执行 brickkit %s 失败（不是退出码非零，是根本没跑起来）：%v", strings.Join(args, " "), err)
		}
	}
	return brickkitResult{stdout: string(out), exitCode: exitCode, err: err}
}

// generatedCompose 读 --dry-run 生成的 docker-compose.yaml 全文。
func (f *fixture) generatedCompose() string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, ".brickkit/generated/docker-compose.yaml"))
	if err != nil {
		f.t.Fatalf("读生成的 docker-compose.yaml 失败：%v", err)
	}
	return string(data)
}

// generatedFile 读 .brickkit/generated/ 下任意相对路径的文件（K8s 场景
// 用，路径形如 "k8s/deployments/foo-bar-1-0-0.yaml"）。
func (f *fixture) generatedFile(relPath string) string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, ".brickkit/generated", relPath))
	if err != nil {
		f.t.Fatalf("读生成文件 %s 失败：%v", relPath, err)
	}
	return string(data)
}

// manifestCache 读 .brickkit/manifests/<name>-<version>.yaml 的缓存内容
// （E-9 验证 Fork 遮蔽用：看缓存里究竟落地的是哪个源提供的那一份）。
func (f *fixture) manifestCache(serviceName, version string) string {
	f.t.Helper()
	path := filepath.Join(f.dir, ".brickkit/manifests", serviceName+"-"+version+".yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		f.t.Fatalf("读 manifest 缓存 %s 失败：%v", path, err)
	}
	return string(data)
}

// minimalComponent 生成一份最小合法 component.yaml，id/version/port 可
// 参数化，其余字段固定——这批断言测的是平台行为，不需要真实业务字段。
func minimalComponent(id, version string, port int) string {
	return "apiVersion: brickkit/v1\n" +
		"kind: Component\n" +
		"metadata:\n" +
		"  id: " + id + "\n" +
		"  name: 断言测试假组件\n" +
		"  version: " + version + "\n" +
		"  description: 平台断言测试用，不是真实业务组件\n" +
		"  license: Apache-2.0\n" +
		"  vendor: brickKit\n" +
		"deployment:\n" +
		"  type: container\n" +
		"  image: brickenterprise/fixture:1.0.0\n" +
		"  port: " + strconv.Itoa(port) + "\n" +
		"healthCheck:\n" +
		"  type: http\n" +
		"  path: /healthz\n"
}
