package gates

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// HostnameFinding 是一处写死在配置里的版本化服务名（如 http://infra-authz-2-0-0:8223）。
//   - Error=true：brickkit.yaml 声明了这个组件，但没有这个版本——组件发版后地址没跟着改，
//     运行期整条链路悄悄 503/403（C18 那类坑）；Expected 是按 brickkit.yaml 应写的服务名。
//   - Error=false（警告）：brickkit.yaml 里根本没有这个组件。项目还没装上它时（06b 之前）
//     这是预期状态，所以只警告，不让门禁变红。
type HostnameFinding struct {
	File        string // 相对 root
	Line        int
	Hostname    string   // 如 "infra-authz-2-0-0"
	ComponentID string   // 已声明时的组件 ID；未声明为空
	Expected    []string // 已声明时，brickkit.yaml 里这个组件全部版本对应的服务名
	Error       bool
}

// versionedHostRe 抽出 URL 里的主机名，且只要以 -<x>-<y>-<z> 结尾的那种（版本化服务名）。
var versionedHostRe = regexp.MustCompile(`https?://([a-z0-9][a-z0-9-]*-[0-9]+-[0-9]+-[0-9]+)(?:[:/"'\s]|$)`)

// hostBaseRe 把服务名拆成 <repo 名>-<x>-<y>-<z>。
var hostBaseRe = regexp.MustCompile(`^(.+)-([0-9]+)-([0-9]+)-([0-9]+)$`)

// ServiceHostnameScan 扫 config/*.yaml（含 config/vars.yaml）全文与根目录各部署文件
// （deploy*.yaml）的 vars: 段，找出版本化服务名，逐个对照 brickkit.yaml 声明的组件版本。
// 服务名规则同 brickKit：`<id>-<version>`，`/` 与 `.` 都换成 `-`。
//
// `$var:` 把字面量集中到了一处，但没有任何东西拿它跟组件当前版本比：brickkit lint 和
// up --dry-run 都不看配置值，bump-version 也不改 config/。这条门禁补上这一项。
// 注释行（及行尾 # 注释）不参与。
func ServiceHostnameScan(root string) ([]HostnameFinding, error) {
	declared, err := declaredServiceNames(filepath.Join(root, "brickkit.yaml"))
	if err != nil {
		return nil, err
	}
	// repo 名（id 的 / 换成 -）→ 组件 ID 与它全部已声明版本的服务名
	byBase := map[string][]string{}
	idOfBase := map[string]string{}
	known := map[string]bool{}
	for _, d := range declared {
		base := strings.ReplaceAll(d.id, "/", "-")
		name := serviceName(d.id, d.version)
		byBase[base] = append(byBase[base], name)
		idOfBase[base] = d.id
		known[name] = true
	}

	var findings []HostnameFinding
	configFiles, err := filepath.Glob(filepath.Join(root, "config", "*.yaml"))
	if err != nil {
		return nil, err
	}
	deployFiles, err := filepath.Glob(filepath.Join(root, "deploy*.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(configFiles)
	sort.Strings(deployFiles)

	scan := func(path string, onlyVars bool) error {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		inVars := false
		for i, line := range strings.Split(string(data), "\n") {
			if onlyVars {
				if line != "" && line[0] != ' ' && line[0] != '\t' && line[0] != '#' {
					inVars = strings.HasPrefix(line, "vars:")
					continue
				}
				if !inVars {
					continue
				}
			}
			text := stripYAMLComment(line)
			for _, m := range versionedHostRe.FindAllStringSubmatch(text, -1) {
				host := m[1]
				if known[host] {
					continue
				}
				f := HostnameFinding{File: rel, Line: i + 1, Hostname: host}
				if bm := hostBaseRe.FindStringSubmatch(host); bm != nil {
					if exp, ok := byBase[bm[1]]; ok {
						f.ComponentID = idOfBase[bm[1]]
						f.Expected = append([]string(nil), exp...)
						sort.Strings(f.Expected)
						f.Error = true
					}
				}
				findings = append(findings, f)
			}
		}
		return nil
	}
	for _, p := range configFiles {
		if err := scan(p, false); err != nil {
			return nil, err
		}
	}
	for _, p := range deployFiles {
		if err := scan(p, true); err != nil {
			return nil, err
		}
	}
	return findings, nil
}

type declaredComponent struct{ id, version string }

// declaredServiceNames 读 brickkit.yaml 的 components:（每项 {id, version, ...}；同一组件可以
// 有多个版本并存）。只读不写，所以这里用 YAML 库没有"往返冲掉注释"的顾虑。
func declaredServiceNames(path string) ([]declaredComponent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读 brickkit.yaml：%w", err)
	}
	var doc struct {
		Components []struct {
			ID      string `yaml:"id"`
			Version string `yaml:"version"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("解析 brickkit.yaml：%w", err)
	}
	var out []declaredComponent
	for _, c := range doc.Components {
		id, version := c.ID, c.Version
		if at := strings.IndexByte(id, '@'); at >= 0 { // 容错：id 写成 x/y@1.2.3
			id, version = id[:at], id[at+1:]
		}
		if id != "" && version != "" {
			out = append(out, declaredComponent{id: id, version: version})
		}
	}
	return out, nil
}

// serviceName 同 brickKit manifest.ServiceName：小写，`/` 与 `.` 换成 `-`。
func serviceName(id, version string) string {
	return strings.NewReplacer("/", "-", ".", "-").Replace(strings.ToLower(id + "-" + version))
}

// stripYAMLComment 去掉整行注释与行尾 " #" 注释（URL 里不会出现 " #"）。
func stripYAMLComment(line string) string {
	if strings.HasPrefix(strings.TrimSpace(line), "#") {
		return ""
	}
	if i := strings.Index(line, " #"); i >= 0 {
		return line[:i]
	}
	return line
}
