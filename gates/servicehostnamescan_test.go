package gates

import (
	"path/filepath"
	"strings"
	"testing"
)

// 版本化服务名（<scope>-<name>-<x>-<y>-<z>，brickKit 的 ServiceName 规则）写在 config/ 与部署文件
// vars: 里，组件一发版就过期——C18 那类"全部 25 处引用漏改"的坑。判据以 brickkit.yaml 为准。

const hostnameBrickkitYAML = `project: demo
components:
  - {id: infra/authz, version: 2.0.1}
  - id: infra/iam-casdoor
    version: 2.0.0
  - {id: mdm/customer, version: 2.0.0, requiredBy: [erp/sales]}
  - {id: mdm/customer, version: 2.1.0}
`

func hostnameFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "brickkit.yaml"), hostnameBrickkitYAML)
	for name, content := range files {
		write(t, filepath.Join(root, name), content)
	}
	return root
}

func errorsOf(fs []HostnameFinding) []HostnameFinding {
	var out []HostnameFinding
	for _, f := range fs {
		if f.Error {
			out = append(out, f)
		}
	}
	return out
}

func TestServiceHostnameScan_与brickkit点yaml一致时零发现(t *testing.T) {
	root := hostnameFixture(t, map[string]string{
		"config/vars.yaml": "AUTHZ_BUNDLE_URL: http://infra-authz-2-0-1:8223/authz/bundle\n" +
			"IAM_JWKS_URL: http://infra-iam-casdoor-2-0-0:8200/.well-known/jwks.json\n",
		// 并存的两个版本，引用其中任一个都对
		"config/erp-sales.yaml": "MDM: http://mdm-customer-2-0-0:8101\nMDM_NEW: http://mdm-customer-2-1-0/x\n",
	})
	fs, err := ServiceHostnameScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 0 {
		t.Fatalf("期望 0 条发现，得到 %+v", fs)
	}
}

func TestServiceHostnameScan_已声明组件版本不一致是错误并点名文件行号与期望(t *testing.T) {
	root := hostnameFixture(t, map[string]string{
		"config/vars.yaml": "# 注释\nPG_HOST: x\nAUTHZ_BUNDLE_URL: http://infra-authz-2-0-0:8223/authz/bundle\n",
	})
	fs, err := ServiceHostnameScan(root)
	if err != nil {
		t.Fatal(err)
	}
	errs := errorsOf(fs)
	if len(errs) != 1 || len(fs) != 1 {
		t.Fatalf("期望恰好 1 条错误，得到 %+v", fs)
	}
	e := errs[0]
	if e.File != "config/vars.yaml" || e.Line != 3 || e.Hostname != "infra-authz-2-0-0" ||
		e.ComponentID != "infra/authz" || strings.Join(e.Expected, ",") != "infra-authz-2-0-1" {
		t.Fatalf("错误应点名 config/vars.yaml:3、主机名、组件与期望值，得到 %+v", e)
	}
}

func TestServiceHostnameScan_未声明的组件只是警告(t *testing.T) {
	root := hostnameFixture(t, map[string]string{
		"config/vars.yaml": "X_URL: http://be-go-infra-1-0-0:8223/authz/bundle\n",
	})
	fs, err := ServiceHostnameScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 || fs[0].Error || fs[0].Hostname != "be-go-infra-1-0-0" || fs[0].Line != 1 {
		t.Fatalf("未声明的组件应恰好 1 条警告（不是错误），得到 %+v", fs)
	}
}

func TestServiceHostnameScan_部署文件只查vars段且跳过注释(t *testing.T) {
	root := hostnameFixture(t, map[string]string{
		"deploy.teardown.yaml": "# 旧地址 http://infra-authz-1-0-0:8223 写在注释里不算\n" +
			"target: docker\n" +
			"vars:\n" +
			"  AUTHZ_BUNDLE_URL: http://infra-authz-1-0-0:8223/authz/bundle   # 过期\n" +
			"  # IAM_JWKS_URL: http://infra-iam-casdoor-1-0-0:8200\n" +
			"components:\n" +
			"  - id: x\n" +
			"    note: http://infra-authz-1-0-0:8223\n",
		"deploy.yaml": "target: docker\ncomponents: []\n",
	})
	fs, err := ServiceHostnameScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 || !fs[0].Error || fs[0].File != "deploy.teardown.yaml" || fs[0].Line != 4 {
		t.Fatalf("部署文件只应查 vars: 段里的非注释行，期望 deploy.teardown.yaml:4 一条错误，得到 %+v", fs)
	}
}

func TestServiceHostnameScan_没有brickkit点yaml报错(t *testing.T) {
	if _, err := ServiceHostnameScan(t.TempDir()); err == nil {
		t.Fatal("缺 brickkit.yaml 时应报错，不能静默通过")
	}
}
