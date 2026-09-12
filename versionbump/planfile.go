package versionbump

import (
	"fmt"
	"strings"
)

// ParsePlanFile 解析一份"计划文件"——调用方（人/AI）手写，描述这一批
// 真正动了代码/测试/文档的组件都是谁、为什么。故意不用 YAML：这份文件
// 只是一次性的输入草稿，不需要跟 component.yaml 一样承载长期维护的
// 结构化数据，用最省心的纯文本格式够了。
//
// 格式：多个块，用单独一行 "---" 分隔；每块三个字段：
//
//	id: erp/inventory
//	version: 1.1.0        # 可选——给了就用这个精确版本，不给就在当前版本上做一次 patch+1
//	reason: 补 XXX 测试，理由可以换行继续写，
//	  直到下一个 "id:"/"version:"/"---" 为止都算 reason 的一部分。
//
// 只需要写"真的动了什么、为什么"的那些根组件——因为依赖它们而需要
// 同步版本号的下游组件，由 ComputeCascade 自动算出来，不需要在计划
// 文件里手写。
func ParsePlanFile(content string) ([]SeedChange, error) {
	var seeds []SeedChange
	var cur *SeedChange
	var reasonLines []string

	flush := func() error {
		if cur == nil {
			return nil
		}
		if cur.ID == "" {
			return fmt.Errorf("有一块缺 id 字段")
		}
		cur.Reason = strings.TrimSpace(strings.Join(reasonLines, "\n"))
		if cur.Reason == "" {
			return fmt.Errorf("组件 %q 缺 reason 字段", cur.ID)
		}
		seeds = append(seeds, *cur)
		cur = nil
		reasonLines = nil
		return nil
	}

	inReason := false
	for _, raw := range strings.Split(content, "\n") {
		line := raw
		trimmed := strings.TrimSpace(line)

		if trimmed == "---" {
			if err := flush(); err != nil {
				return nil, err
			}
			inReason = false
			continue
		}
		if trimmed == "" {
			if inReason {
				reasonLines = append(reasonLines, "")
			}
			continue
		}

		switch {
		case strings.HasPrefix(trimmed, "id:"):
			if err := flush(); err != nil {
				return nil, err
			}
			cur = &SeedChange{ID: strings.TrimSpace(strings.TrimPrefix(trimmed, "id:"))}
			inReason = false
		case strings.HasPrefix(trimmed, "version:"):
			if cur == nil {
				return nil, fmt.Errorf("version: 字段出现在 id: 之前")
			}
			cur.NewVer = strings.TrimSpace(strings.TrimPrefix(trimmed, "version:"))
			inReason = false
		case strings.HasPrefix(trimmed, "reason:"):
			if cur == nil {
				return nil, fmt.Errorf("reason: 字段出现在 id: 之前")
			}
			reasonLines = []string{strings.TrimSpace(strings.TrimPrefix(trimmed, "reason:"))}
			inReason = true
		default:
			if inReason {
				reasonLines = append(reasonLines, line)
			} else {
				return nil, fmt.Errorf("看不懂这一行（不是 id:/version:/reason:/---，也不在 reason 续行里）：%q", line)
			}
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(seeds) == 0 {
		return nil, fmt.Errorf("计划文件里一条变更都没有")
	}
	return seeds, nil
}
