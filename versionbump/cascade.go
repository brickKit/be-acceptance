package versionbump

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Change 是一条要落地的版本变更——要么是调用方直接指定的"根变更"
// （真的动了代码/测试/文档，有一段人写的理由），要么是级联算出来的
// "依赖版本号同步"（理由是机械生成的模板句）。
type Change struct {
	ID      string
	OldVer  string
	NewVer  string
	Reason  string
	IsCascade bool // true = 因为依赖的组件版本变了而被动跟着升级，不是本来就要改
}

// BumpPatch 把 "1.0.11" 变成 "1.0.12"——本项目的版本号升级统一走
// patch 位（总纲"每个改动都跳版本号"这条纪律里，语义大版本从没在这类
// 日常改动里用到过，minor/major 需要动 metadata.id 之外的破坏性判断，
// 这个工具不替调用方做那个判断，只做"下一个 patch"这一件确定的事）。
func BumpPatch(version string) (string, error) {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("版本号 %q 不是 x.y.z 形状", version)
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return "", fmt.Errorf("版本号 %q 的 patch 段不是数字：%w", version, err)
	}
	return fmt.Sprintf("%s.%s.%d", parts[0], parts[1], patch+1), nil
}

// SeedChange 是调用方（人/AI）明确指定的一条根变更的输入形状——见
// planfile.go 的 planfile 格式。
type SeedChange struct {
	ID     string
	Reason string
	// NewVer 非空时用它做显式目标版本（比如需要跳过 patch 走 minor）；
	// 为空时对 registry 里的当前版本做一次 BumpPatch。
	NewVer string
}

// ComputeCascade 从一批根变更出发，反复在"谁依赖了刚变了版本号的组件"
// 这张反向依赖图上找下一圈，直到不再有新组件被牵连（不动点）——同一圈
// 里被多个刚变更的依赖同时牵连的组件只升级一次、理由里把它依赖的全部
// 几个一起点名，不会因为它依赖两个刚变的组件就被处理两次。
//
// 返回值 order 是"根变更在前、后面按被牵连的先后顺序排列"的完整变更
// 列表——这个顺序只是给人看的可读性（哪个在先因为哪个在后），不是正确
// 性所需要的拓扑序：每个组件的新版本号在这一步就已经全部定下来了，
// Apply 阶段谁先谁后写文件不影响结果。
func ComputeCascade(reg map[string]*Component, seeds []SeedChange) ([]Change, error) {
	changed := map[string]string{} // id -> newVersion
	var order []Change

	for _, s := range seeds {
		comp, ok := reg[s.ID]
		if !ok {
			return nil, fmt.Errorf("根变更引用的组件 %q 在 registry 里找不到（component.yaml 是不是路径错了/还没建）", s.ID)
		}
		newVer := s.NewVer
		if newVer == "" {
			v, err := BumpPatch(comp.Version)
			if err != nil {
				return nil, err
			}
			newVer = v
		}
		if _, dup := changed[s.ID]; dup {
			return nil, fmt.Errorf("组件 %q 在根变更列表里出现了不止一次", s.ID)
		}
		changed[s.ID] = newVer
		order = append(order, Change{ID: s.ID, OldVer: comp.Version, NewVer: newVer, Reason: s.Reason, IsCascade: false})
	}

	// 反向依赖图：dep id -> 依赖它的组件 id 列表。
	revDeps := map[string][]string{}
	for id, comp := range reg {
		for _, dep := range comp.DepIDs {
			revDeps[dep] = append(revDeps[dep], id)
		}
	}

	frontier := make([]string, 0, len(changed))
	for id := range changed {
		frontier = append(frontier, id)
	}

	for len(frontier) > 0 {
		// candidates：这一圈里因为 frontier 里的组件而被牵连、但自己
		// 还没变过的组件，连同它到底是因为哪几个刚变的依赖被牵连的。
		candidates := map[string][]string{}
		seenDep := map[string]map[string]bool{}
		for _, changedID := range frontier {
			for _, dependent := range revDeps[changedID] {
				if _, already := changed[dependent]; already {
					continue
				}
				if seenDep[dependent] == nil {
					seenDep[dependent] = map[string]bool{}
				}
				if !seenDep[dependent][changedID] {
					seenDep[dependent][changedID] = true
					candidates[dependent] = append(candidates[dependent], changedID)
				}
			}
		}
		if len(candidates) == 0 {
			break
		}

		var ids []string
		for id := range candidates {
			ids = append(ids, id)
		}
		sort.Strings(ids)

		var nextFrontier []string
		for _, id := range ids {
			comp := reg[id]
			newVer, err := BumpPatch(comp.Version)
			if err != nil {
				return nil, err
			}
			refs := candidates[id]
			sort.Strings(refs)
			var cited []string
			for _, r := range refs {
				cited = append(cited, fmt.Sprintf("%s@%s", r, changed[r]))
			}
			reason := fmt.Sprintf("依赖版本号同步跟进 %s，无破坏性变更。", strings.Join(cited, "、"))
			changed[id] = newVer
			order = append(order, Change{ID: id, OldVer: comp.Version, NewVer: newVer, Reason: reason, IsCascade: true})
			nextFrontier = append(nextFrontier, id)
		}
		frontier = nextFrontier
	}

	return order, nil
}
