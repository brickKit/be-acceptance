// Package closedloop_test 固化档 0 出档的六项验收（Task 17）。
//
// ⚠️ 这些测试不 import 任何组件仓库的生成代码——be-acceptance 自己的
// import-scan 门禁（铁律六）不许这么做。gRPC 调用改用外部 grpcurl 二进制
// 打进程黑盒（读 .proto 文件只是文件路径引用，不构成 Go import）。
//
// 这批测试要求：真实起着的 mdm-customer（brickkit up 过）、docker、
// grpcurl、brickkit 三个 CLI 在 PATH 上。缺哪个就跳过对应测试，不是
// 放宽断言——这与 be-sdk-go 自己的 TEST_PG_DSN 未设置时 t.Skip 是同一类
// "需要真实外部环境，环境不在就跳过"，不是为了绕开一个红的实现。
//
// 用法：cd tools/be-acceptance && go test ./closedloop/ -run 'Test档0' -v -count=1
package closedloop_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// ⚠️ 这三个常量按组件当前版本号硬编码容器/镜像名——mdm-customer 每次
// 版本升级（包括纯粹的镜像重新构建，不影响功能的那种）都要跟着改一遍。
// 阶段三 Task 4 真的因为这几个常量停在 v1.0.0、而 mdm-customer 早就
// 升到 v1.0.2，导致 Test档0_1/2/3/5 全部 SKIP、Test档0_4 直接 FAIL——
// 且这条漂移在 v1.0.1 那次升级时就已经发生，一直没人重跑 tier0 才没
// 被发现（实测踩坑记录类别 F）。⚠️ 阶段三 Task 6 又真实发生了一次
// （1.0.2 → 1.0.3）；阶段三收官后的 SOP-W-7 种子数据样板（mdm-customer
// 补 make seed）又发生第三次（1.0.3 → 1.0.4，Test档0_4 直接 FAIL，
// 这次没有影响 Test档0_1/2/3/5——它们判据里的容器/镜像名也读这三个
// 常量，只是没有测试真的先跑一遍暴露出来，直到这次才发现是真漏改）——
// 这不是假设性的风险，是这份手动同步的负担会反复兑现；接受它（不改成
// 动态读 component.yaml 解析版本号）是 SOP-P 判据下的刻意选择：这只是
// 六个硬编码字符串，加一层解析代码换来的是"版本号对不上"这类错误从
// 编译期挪到运行期，不划算。⚠️ 第四次真实发生（1.0.4 → 1.0.5，种子
// 数据丰富度批量整改期间），这次是跑完整个 `go test ./...`（而不是单独
// 重跑 `Test档0`）才暴露——本仓库自己 `bump-version` 那批工作没有覆盖
// 这三个常量，是刻意的：`bump-version` 只管 `components/*/*/
// component.yaml` 互相引用的那张依赖图，这三个常量是**另一个仓库**
// （be-acceptance 自己）里的测试夹具，不在那张图里，仍然要靠这条注释
// 提醒人工同步。**每次给 mdm-customer 出新版本，先来改这三行，再跑
// tier0。**
const (
	mdmContainer      = "brickkit-be-assembly-standard-mdm-customer-1-0-5-1"
	mdmMigContainer   = "brickkit-be-assembly-standard-mdm-customer-1-0-5-migration-1"
	postgresContainer = "be-postgres"
	mdmImage          = "brickenterprise/mdm-customer:1.0.5"
	httpBase          = "http://localhost:8080"
	grpcServiceName   = "mdm.customer.v1.CustomerService"
)

// repoRoot 返回装配仓库根目录的绝对路径。go test 的 CWD 是包目录本身
// （tools/be-acceptance/closedloop/），不是模块根——所以是 3 层 ..，
// 不是 2 层（第一次写成 2 层，实测直接落在 tools/ 上，找不到
// brickkit.yaml，被自己的 t.Skip 挡住——这条路径必须真的跑一次测试才
// 会暴露，静态看代码看不出差一层）。
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

func mdmCustomerContractsDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(repoRoot(t), "components/mdm/customer/contracts/mdm/customer/v1")
	if _, err := os.Stat(filepath.Join(dir, "customer.proto")); err != nil {
		t.Skipf("找不到 customer.proto（%s）：%v", dir, err)
	}
	return dir
}

// protobufWKTImportPath 找 GOPATH 模块缓存里 google.golang.org/protobuf 的
// well-known types（google/protobuf/timestamp.proto 等）目录——版本号跟着
// mdm-customer/go.mod 实际锁定的那个走，不在这里另起一份写死的版本号。
func protobufWKTImportPath(t *testing.T) string {
	t.Helper()
	gomod, err := os.ReadFile(filepath.Join(repoRoot(t), "components/mdm/customer/go.mod"))
	if err != nil {
		t.Skipf("读不到 mdm-customer/go.mod：%v", err)
	}
	m := regexp.MustCompile(`google\.golang\.org/protobuf (v\S+)`).FindSubmatch(gomod)
	if m == nil {
		t.Skip("go.mod 里没找到 google.golang.org/protobuf 版本号")
	}
	version := string(m[1])
	gopath, err := exec.Command("go", "env", "GOPATH").Output()
	if err != nil {
		t.Skipf("go env GOPATH 失败：%v", err)
	}
	dir := filepath.Join(strings.TrimSpace(string(gopath)), "pkg/mod/google.golang.org/protobuf@"+version)
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("protobuf@%s 不在模块缓存里（%s）：%v", version, dir, err)
	}
	return dir
}

func requireCommand(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("需要 %s 在 PATH 上：%v", name, err)
	}
	return path
}

// dockerHealth 返回容器的健康检查状态（"healthy"/"unhealthy"/"starting"/...），
// 容器不存在或 docker 不在 PATH 上则跳过。
func dockerHealth(t *testing.T, container string) string {
	t.Helper()
	requireCommand(t, "docker")
	out, err := exec.Command("docker", "inspect", "-f", "{{.State.Health.Status}}", container).Output()
	if err != nil {
		t.Skipf("容器 %s 不存在或 docker inspect 失败（先 brickkit up 起来）：%v", container, err)
	}
	return strings.TrimSpace(string(out))
}

// dockerContainerIP 拿容器在 brickKit 生成的桥接网络里的 IP——gRPC 额外
// 端口没有宿主机映射（见 docs/dev/实测踩坑记录.md D7），只能这样连。
func dockerContainerIP(t *testing.T, container string) string {
	t.Helper()
	requireCommand(t, "docker")
	out, err := exec.Command("docker", "inspect", "-f",
		`{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}`, container).Output()
	if err != nil {
		t.Skipf("拿不到容器 %s 的 IP：%v", container, err)
	}
	ip := strings.TrimSpace(string(out))
	if ip == "" {
		t.Skipf("容器 %s 没有网络 IP（还没起来？）", container)
	}
	return ip
}

func grpcurlJSON(t *testing.T, addr, method, data string) map[string]any {
	t.Helper()
	grpcurl := requireCommand(t, "grpcurl")
	contractsDir := mdmCustomerContractsDir(t)
	wkt := protobufWKTImportPath(t)

	args := []string{
		"-plaintext",
		"-import-path", contractsDir,
		"-import-path", wkt,
		"-proto", "customer.proto",
	}
	if data != "" {
		args = append(args, "-d", data)
	}
	args = append(args, addr, method)

	cmd := exec.Command(grpcurl, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("grpcurl %s 失败：%v\n输出：%s", method, err, out)
	}
	var result map[string]any
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("grpcurl %s 的输出不是合法 JSON：%v\n输出：%s", method, err, out)
	}
	return result
}

// grpcurlList 单独处理，因为 `list` 子命令的输出是纯文本（一行一个
// 方法名），不是 JSON。
func grpcurlList(t *testing.T, addr, service string) []string {
	t.Helper()
	grpcurl := requireCommand(t, "grpcurl")
	contractsDir := mdmCustomerContractsDir(t)
	wkt := protobufWKTImportPath(t)

	cmd := exec.Command(grpcurl,
		"-plaintext",
		"-import-path", contractsDir,
		"-import-path", wkt,
		"-proto", "customer.proto",
		addr, "list", service,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("grpcurl list 失败：%v\n输出：%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var methods []string
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			methods = append(methods, l)
		}
	}
	return methods
}

// ────────────────────────────────────────────────────────────────
// 验收 1：单独 brickkit up 起来
// ────────────────────────────────────────────────────────────────

func Test档0_1_单独up起来(t *testing.T) {
	status := dockerHealth(t, mdmContainer)
	if status != "healthy" {
		t.Fatalf("期望 %s 容器 healthy，实际 %q", mdmContainer, status)
	}
}

// ────────────────────────────────────────────────────────────────
// 验收 2：curl 打通 HTTP
// ────────────────────────────────────────────────────────────────

// ⚠️ 阶段三 Task 7 之后：/mdm/customer/customers 的创建/列表两条路由是
// 真实权限键（mdm.customer.create/view），iamJwksUrl 也已经配成真实签发方
// （infra-iam-casdoor）——RequirePermission 走的是真判定链，不再是"没配
// iamJwksUrl"的 fail-closed stub。这条测试不带 Authorization header，
// 判定链第 2 步（bearerToken 取不到）就返回 401，不会走到 403 那一步——
// 401 与 403 的区别正是"没证明你是谁"与"证明了但没权限"，本条只测前者。
// 带真实签名 JWT 的端到端权限验证（能不能拿到 200/403）属于具体业务
// 规则，不在档 0 平台级冒烟测试的职责范围内（档 0 测的是"brickkit 本身
// 能不能把组件跑起来"）。
func Test档0_2_curl打通HTTP(t *testing.T) {
	if status := dockerHealth(t, mdmContainer); status != "healthy" {
		t.Skipf("mdm-customer 容器不 healthy（%s），先 brickkit up", status)
	}
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get(httpBase + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz 失败：%v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz 期望 200（besdk.NewGinEngine 统一挂的健康检查，不受权限判定影响），得到 %d", resp.StatusCode)
	}

	idemKey := fmt.Sprintf("tier0-http-%d", time.Now().UnixNano())
	body := fmt.Sprintf(`{"idempotency_key":%q,"code":%q,"name":"档0验收客户","credit_limit":"50000.00"}`,
		idemKey, "C-TIER0-"+idemKey)
	resp, err = client.Post(httpBase+"/mdm/customer/customers", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /mdm/customer/customers 失败：%v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("mdm.customer.create 不带 Authorization 时期望 401（缺少凭据，真判定链第 2 步），得到 %d", resp.StatusCode)
	}

	resp, err = client.Get(httpBase + "/mdm/customer/customers?page_size=10")
	if err != nil {
		t.Fatalf("GET 列表失败：%v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("mdm.customer.view 不带 Authorization 时期望 401（缺少凭据，真判定链第 2 步），得到 %d", resp.StatusCode)
	}
}

// ────────────────────────────────────────────────────────────────
// 验收 3：grpcurl 打通 Get/List/BatchGet
// ────────────────────────────────────────────────────────────────

func Test档0_3_grpcurl打通三个rpc(t *testing.T) {
	if status := dockerHealth(t, mdmContainer); status != "healthy" {
		t.Skipf("mdm-customer 容器不 healthy（%s），先 brickkit up", status)
	}
	ip := dockerContainerIP(t, mdmContainer)
	addr := ip + ":9090"

	methods := grpcurlList(t, addr, grpcServiceName)
	if len(methods) != 8 {
		t.Fatalf("期望 %s 暴露 8 个 rpc，实际 %d 个：%v", grpcServiceName, len(methods), methods)
	}

	idemKey := fmt.Sprintf("tier0-grpc-%d", time.Now().UnixNano())
	createResp := grpcurlJSON(t, addr, grpcServiceName+"/Create", fmt.Sprintf(
		`{"idempotency_key":%q,"name":"档0grpc验收"}`, idemKey))
	// CreateResponse 把客户对象包在 "customer" 字段下（不像 REST 那样摊平），
	// 契约见 contracts/mdm/customer/v1/customer.proto 的 CreateResponse。
	customer, _ := createResp["customer"].(map[string]any)
	id, _ := customer["id"].(string)
	if id == "" {
		t.Fatalf("Create 响应里没有 customer.id：%v", createResp)
	}

	got := grpcurlJSON(t, addr, grpcServiceName+"/Get", fmt.Sprintf(`{"id":%q}`, id))
	if got["id"] != id {
		t.Fatalf("Get 期望拿到 id=%s，实际：%v", id, got)
	}

	list := grpcurlJSON(t, addr, grpcServiceName+"/List", `{"page_size":10}`)
	if _, ok := list["customers"]; !ok {
		t.Fatalf("List 响应里没有 customers 字段：%v", list)
	}

	batch := grpcurlJSON(t, addr, grpcServiceName+"/BatchGet",
		fmt.Sprintf(`{"ids":[%q,"999999999"]}`, id))
	missing, _ := batch["missingIds"].([]any)
	found := false
	for _, m := range missing {
		if fmt.Sprint(m) == "999999999" {
			found = true
		}
	}
	if !found {
		t.Fatalf("BatchGet 的 missingIds 应该包含 999999999，实际：%v", batch["missingIds"])
	}
}

// ────────────────────────────────────────────────────────────────
// 验收 4：迁移被平台单独调起且可重跑
// ────────────────────────────────────────────────────────────────

func Test档0_4_迁移可重跑(t *testing.T) {
	brickkit := requireCommand(t, "brickkit")
	requireCommand(t, "docker")
	root := repoRoot(t)

	run := func(args ...string) []byte {
		cmd := exec.Command(brickkit, args...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("brickkit %s 失败：%v\n输出：%s", strings.Join(args, " "), err, out)
		}
		return out
	}

	run("down")
	run("up")

	out, err := exec.Command("docker", "logs", mdmMigContainer).CombinedOutput()
	if err != nil {
		t.Fatalf("docker logs %s 失败：%v", mdmMigContainer, err)
	}
	if !strings.Contains(string(out), "迁移完成") {
		t.Fatalf("期望迁移日志包含「迁移完成」，实际：%s", out)
	}

	if status := dockerHealth(t, mdmContainer); status != "healthy" {
		t.Fatalf("第二次 up 之后期望 mdm-customer healthy，实际 %q", status)
	}
}

// ────────────────────────────────────────────────────────────────
// 验收 5：/healthz 只查本进程（PG 停了也该 200）
// ────────────────────────────────────────────────────────────────

func Test档0_5_healthz不查库(t *testing.T) {
	requireCommand(t, "docker")
	if status := dockerHealth(t, mdmContainer); status != "healthy" {
		t.Skipf("mdm-customer 容器不 healthy（%s），先 brickkit up", status)
	}

	if out, err := exec.Command("docker", "stop", postgresContainer).CombinedOutput(); err != nil {
		t.Fatalf("docker stop %s 失败：%v\n%s", postgresContainer, err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command("docker", "start", postgresContainer).CombinedOutput(); err != nil {
			t.Errorf("清理时 docker start %s 失败（需要手动恢复！）：%v\n%s", postgresContainer, err, out)
		}
	})

	time.Sleep(3 * time.Second)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(httpBase + "/healthz")
	if err != nil {
		t.Fatalf("PG 停了之后 GET /healthz 失败（不该失败）：%v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PG 停了之后 /healthz 期望仍是 200，得到 %d——说明实现里查了库（§12.3.6）", resp.StatusCode)
	}

	if status := dockerHealth(t, mdmContainer); status != "healthy" {
		t.Fatalf("PG 停着的这段时间容器不该重启/crash，实际状态 %q", status)
	}
}

// ────────────────────────────────────────────────────────────────
// 验收 6：镜像里有 shell + wget
// ────────────────────────────────────────────────────────────────

func Test档0_6_镜像有shell和wget(t *testing.T) {
	requireCommand(t, "docker")
	out, err := exec.Command("docker", "run", "--rm", "--entrypoint", "sh",
		mdmImage, "-c", "wget --version | head -1").CombinedOutput()
	if err != nil {
		t.Skipf("镜像 %s 不在本地（先 make image）：%v", mdmImage, err)
	}
	if !strings.Contains(string(out), "Wget") {
		t.Fatalf("期望输出包含 wget 版本号，实际：%s", out)
	}
}
