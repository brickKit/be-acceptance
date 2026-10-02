package gates

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ConfigKeyViolation 是一个 configSchema 键违反了命名规则。
type ConfigKeyViolation struct {
	File string // 相对装配根，如 "components/mdm/customer/component.yaml"
	Line int    // 键所在行
	Key  string
	// Rule 是违反的规则：
	//   naming          —— 不满足 ^[A-Z][A-Z0-9_]*$（键就是环境变量名）
	//   endpoint-suffix —— 以 _ENDPOINT 结尾（平台保留后缀，平台的值会覆盖它）
	//   reserved        —— 撞了平台保留名
	Rule string
	// Pending：组件（不含外壳）的 metadata.version 主版本号小于 2，即 06b 里
	// 还没迁移到 2.x 的组件。违规照样报出来（那是事实），由调用方决定计不计入失败。
	// 外壳本来就在 1.x，永远是 false。
	Pending bool
}

// configKeyRe：配置键就是注入进程的环境变量名（docs/en/01-conventions/04-configuration.md）。
var configKeyRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// reservedConfigKeys 是平台自己注入的名字（brickkit docs
// 06-architecture/03-env-injection-contract 的 Reserved names 一节）。
// 撞上之后平台的值获胜，只有一条警告，组件永远拿不到自己的值。
var reservedConfigKeys = map[string]bool{
	"COMPONENT_ID":                   true,
	"COMPONENT_VERSION":              true,
	"PORT":                           true,
	"BRICKKIT_SERVED_MEMBERS":        true,
	"BRICKKIT_SERVED_MEMBERS_CONFIG": true,
}

// ConfigKeyScan 检查全部 components/<scope>/<name>/component.yaml 与
// shell/<scope>/<name>/component.yaml 的 configSchema.properties 键名。
//
// 存在的理由：brickKit 把键名原样当环境变量注入，驼峰键 lint 与 up 都静默接受
// （06b 试点 V-01 实测），只有一次性的迁移脚本拦得住；12 个组件并行迁移时需要
// 一个常设门禁。
func ConfigKeyScan(root string) ([]ConfigKeyViolation, error) {
	var manifests []manifestRef
	for _, dir := range componentDirs(filepath.Join(root, "components")) {
		manifests = append(manifests, manifestRef{filepath.Join(dir, "component.yaml"), false})
	}
	shells, err := filepath.Glob(filepath.Join(root, "shell", "*", "*", "component.yaml"))
	if err != nil {
		return nil, err
	}
	for _, p := range shells {
		manifests = append(manifests, manifestRef{p, true})
	}

	var out []ConfigKeyViolation
	for _, m := range manifests {
		vs, err := configKeyViolations(root, m.path, m.isShell)
		if err != nil {
			return nil, err
		}
		out = append(out, vs...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

// manifestRef 是一份要扫的清单；外壳与组件的过渡期判据不同。
type manifestRef struct {
	path    string
	isShell bool
}

func configKeyViolations(root, path string, isShell bool) ([]ConfigKeyViolation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	rel = filepath.ToSlash(rel)

	// 用 yaml.Node 解析 properties：保留键的行号，报告能直接点到那一行。
	var doc struct {
		Metadata struct {
			Version string `yaml:"version"`
		} `yaml:"metadata"`
		ConfigSchema struct {
			Properties yaml.Node `yaml:"properties"`
		} `yaml:"configSchema"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s 不是合法的 YAML：%w", rel, err)
	}
	pending := !isShell && majorBelow2(doc.Metadata.Version)

	props := doc.ConfigSchema.Properties
	if props.Kind != yaml.MappingNode {
		return nil, nil
	}
	var out []ConfigKeyViolation
	for i := 0; i+1 < len(props.Content); i += 2 {
		k := props.Content[i]
		for _, rule := range configKeyRules(k.Value) {
			out = append(out, ConfigKeyViolation{File: rel, Line: k.Line, Key: k.Value, Rule: rule, Pending: pending})
		}
	}
	return out, nil
}

// configKeyRules 返回一个键违反的全部规则（可能不止一条）。
func configKeyRules(key string) []string {
	var rules []string
	if !configKeyRe.MatchString(key) {
		rules = append(rules, "naming")
	}
	if strings.HasSuffix(key, "_ENDPOINT") {
		rules = append(rules, "endpoint-suffix")
	}
	if reservedConfigKeys[key] {
		rules = append(rules, "reserved")
	}
	return rules
}

// majorBelow2：版本号主版本号小于 2。读不出主版本号时按"不是迁移中"处理——
// 宁可严格，也不让一个写坏的版本号把组件放进宽松名单。
func majorBelow2(version string) bool {
	major, _, ok := strings.Cut(strings.TrimSpace(version), ".")
	if !ok {
		return false
	}
	n, err := strconv.Atoi(major)
	if err != nil {
		return false
	}
	return n < 2
}
