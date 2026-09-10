package gates

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// EventsBreakingViolation 是一条事件契约破坏性变更：某条签名在 main
// 版本里有、当前工作区里没了——意味着删了字段、改了类型，或整个删掉了
// 一个 subject。
type EventsBreakingViolation struct {
	Component string // 组件 ID，如 "crm/opportunity"
	File      string // 相对组件目录，如 "contracts/events/opportunity.events.json"
	Missing   string // 丢失的签名，如 "event.crm.opportunity.won.v1.payload.owner_id::type=string"
}

// EventsBreakingScan 对每个组件的 contracts/events/*.json 做"只增不删不改"
// （设计书 §3.10、决策 19）检查：把当前工作区版本与该组件 git main 分支上
// 的版本各自拍平成一组签名，main 版本里的每一条签名都必须在当前版本里
// 仍然存在。追加新字段 / 新事件只会产生新签名，不触发违规。
//
// ⚠️ 存在的理由：`buf breaking` 只认 `.proto`，`contracts/events/*.json`
// 一直没有任何机器门禁，靠人工评审兜底（总纲 SOP-W-8、W-6）。这个 gate
// 补的就是这个空白。
//
// 拿不到 main 基线（新文件 / 组件仓库没有 main ref / 目录不是 git 仓库）
// 就跳过那个文件——同 `buf breaking` 在没有 `.git` 时的行为，不因为"没有
// 可比对象"就把门禁判红。main 上的旧版本自己解析不了也跳过（那不是本次
// 改动的错）。
func EventsBreakingScan(root string) ([]EventsBreakingViolation, error) {
	componentsDir := filepath.Join(root, "components")
	var violations []EventsBreakingViolation

	for _, compDir := range componentDirs(componentsDir) {
		id := componentID(componentsDir, compDir)
		eventsDir := filepath.Join(compDir, "contracts", "events")
		entries, err := os.ReadDir(eventsDir)
		if err != nil {
			continue // 没有 contracts/events/ 目录（如前端组件）
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			rel := filepath.ToSlash(filepath.Join("contracts", "events", e.Name()))

			newData, err := os.ReadFile(filepath.Join(compDir, filepath.FromSlash(rel)))
			if err != nil {
				return nil, fmt.Errorf("读 %s/%s：%w", id, rel, err)
			}
			oldData, ok := gitShowMain(compDir, rel)
			if !ok {
				continue // 没有基线可比
			}

			oldSigs, err := eventSignatures(oldData)
			if err != nil {
				continue // main 上的旧版本解析不了，不是本次改动的责任
			}
			newSigs, err := eventSignatures(newData)
			if err != nil {
				return nil, fmt.Errorf("%s/%s 不是合法的 events JSON：%w", id, rel, err)
			}

			for _, sig := range missingSignatures(oldSigs, newSigs) {
				violations = append(violations, EventsBreakingViolation{
					Component: id, File: rel, Missing: sig,
				})
			}
		}
	}

	sort.Slice(violations, func(i, j int) bool {
		a, b := violations[i], violations[j]
		if a.Component != b.Component {
			return a.Component < b.Component
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Missing < b.Missing
	})
	return violations, nil
}

// missingSignatures 返回 old 里有、new 里没有的签名（排序后）。
func missingSignatures(old, new map[string]bool) []string {
	var missing []string
	for sig := range old {
		if !new[sig] {
			missing = append(missing, sig)
		}
	}
	sort.Strings(missing)
	return missing
}

// gitShowMain 取组件仓库 main 分支上某个文件的内容。拿不到（没有 main
// ref、文件在 main 上不存在、目录不是 git 仓库）返回 ok=false。
func gitShowMain(compDir, relPath string) ([]byte, bool) {
	out, err := exec.Command("git", "-C", compDir, "show", "main:"+relPath).Output()
	if err != nil {
		return nil, false
	}
	return out, true
}

// eventSignatures 把一份 events JSON 拍平成一组签名。判据是 §3.10 的
// "只增不删不改"：允许追加，禁止删字段、改类型——所以签名要覆盖到
// "每个字段路径 + 它的类型"这个粒度，外加"每个 subject 存在"。
//
// 签名形态：
//
//	event::<subject>                          — 这个事件还在
//	event.<subject>.payload.<路径>::type=<t>  — 这个字段还在、类型没变
//	envelope.<路径>::type=<t>                 — 信封字段（所有组件共用同一份信封形状）
func eventSignatures(data []byte) (map[string]bool, error) {
	var doc struct {
		Envelope struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"envelope"`
		Events []struct {
			Subject string          `json:"subject"`
			Payload json.RawMessage `json:"payload"`
		} `json:"events"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}

	sigs := map[string]bool{}
	for name, raw := range doc.Envelope.Properties {
		schemaSigs(raw, "envelope."+name, sigs)
	}
	for _, ev := range doc.Events {
		if ev.Subject == "" {
			continue
		}
		sigs["event::"+ev.Subject] = true
		if len(ev.Payload) > 0 {
			schemaSigs(ev.Payload, "event."+ev.Subject+".payload", sigs)
		}
	}
	return sigs, nil
}

// schemaSigs 递归走一个 JSON-Schema-ish 节点（type / properties / items），
// 每遇到一个带 type 的节点就记一条 "<path>::type=<t>"。
//
// 用 map[string]RawMessage 逐键取值而不是一次性 Unmarshal 进结构体——
// 这样一个形状奇怪的 type（比如 ["string","null"] 这种复合类型）不会
// 让整个节点解析失败、连带把 properties/items 也漏掉。
func schemaSigs(raw json.RawMessage, path string, sigs map[string]bool) {
	var node map[string]json.RawMessage
	if err := json.Unmarshal(raw, &node); err != nil {
		return
	}
	if t, ok := node["type"]; ok {
		var s string
		if json.Unmarshal(t, &s) == nil {
			sigs[path+"::type="+s] = true
		} else {
			sigs[path+"::type="+strings.Join(strings.Fields(string(t)), "")] = true
		}
	}
	if p, ok := node["properties"]; ok {
		var props map[string]json.RawMessage
		if json.Unmarshal(p, &props) == nil {
			for name, child := range props {
				schemaSigs(child, path+"."+name, sigs)
			}
		}
	}
	if it, ok := node["items"]; ok {
		schemaSigs(it, path+"[]", sigs)
	}
}
