package gates

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrUnknownComponent：--only 给的组件在装配根下找不到（或有歧义）。CLI 把它当用法错误，exit 2。
var ErrUnknownComponent = errors.New("找不到组件")

// ComponentRef 是 --only 选中的一个组件或外壳。
type ComponentRef struct {
	Rel     string // 相对装配根的目录，如 "components/mdm/customer"、"shell/be/go-core"
	ID      string // 按目录推出的 ID（与各 gate 报告里用的一致），如 "mdm/customer"
	IsShell bool
}

// ResolveComponent 把 --only 的值认成一个组件：
//   - 目录路径 components/<scope>/<name> 或 shell/<scope>/<name>（结尾的 / 可有可无）；
//   - metadata.id（如 mdm/customer）：先看 components/<id>、shell/<id>，再找 metadata.id 等于它的清单
//     （fork 的目录名可以和 metadata.id 不同）。
//
// 目录下必须有 component.yaml。找不到、或同一个 ID 对上不止一个目录 → ErrUnknownComponent。
func ResolveComponent(root, only string) (ComponentRef, error) {
	o := strings.TrimSuffix(filepath.ToSlash(strings.TrimSpace(only)), "/")
	parts := strings.Split(o, "/")
	bad := o == "" || strings.HasPrefix(o, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			bad = true
		}
	}
	if bad {
		return ComponentRef{}, fmt.Errorf("%w：%q 不是 <scope>/<name>、components/<scope>/<name> 或 shell/<scope>/<name>", ErrUnknownComponent, only)
	}
	hasManifest := func(rel string) bool {
		st, err := os.Stat(filepath.Join(root, rel, "component.yaml"))
		return err == nil && !st.IsDir()
	}
	refOf := func(rel string) ComponentRef {
		base, id, _ := strings.Cut(rel, "/")
		return ComponentRef{Rel: rel, ID: id, IsShell: base == "shell"}
	}

	if len(parts) == 3 && (parts[0] == "components" || parts[0] == "shell") {
		if !hasManifest(o) {
			return ComponentRef{}, fmt.Errorf("%w：%s 下没有 component.yaml", ErrUnknownComponent, o)
		}
		return refOf(o), nil
	}
	if len(parts) != 2 {
		return ComponentRef{}, fmt.Errorf("%w：%q 不是 <scope>/<name>、components/<scope>/<name> 或 shell/<scope>/<name>", ErrUnknownComponent, only)
	}

	seen := map[string]bool{}
	var hits []string
	add := func(rel string) {
		if !seen[rel] {
			seen[rel] = true
			hits = append(hits, rel)
		}
	}
	for _, base := range []string{"components", "shell"} {
		if rel := base + "/" + o; hasManifest(rel) {
			add(rel)
		}
	}
	for _, base := range []string{"components", "shell"} {
		manifests, _ := filepath.Glob(filepath.Join(root, base, "*", "*", "component.yaml"))
		for _, p := range manifests {
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			var m struct {
				Metadata struct {
					ID string `yaml:"id"`
				} `yaml:"metadata"`
			}
			if yaml.Unmarshal(data, &m) != nil || m.Metadata.ID != o { // 别的组件的清单坏了不影响认这一个
				continue
			}
			rel, err := filepath.Rel(root, filepath.Dir(p))
			if err == nil {
				add(filepath.ToSlash(rel))
			}
		}
	}
	switch len(hits) {
	case 0:
		return ComponentRef{}, fmt.Errorf("%w：%s（components/、shell/ 下没有这个目录，也没有 metadata.id 是它的清单）", ErrUnknownComponent, o)
	case 1:
		return refOf(hits[0]), nil
	default:
		return ComponentRef{}, fmt.Errorf("%w：%s 有歧义，对上了 %s；用目录路径指定", ErrUnknownComponent, o, strings.Join(hits, "、"))
	}
}
