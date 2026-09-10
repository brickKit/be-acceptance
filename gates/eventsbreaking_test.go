package gates

import (
	"os/exec"
	"path/filepath"
	"testing"
)

const eventsBase = `{
  "envelope": { "properties": {
    "subject": { "type": "string" },
    "version": { "type": "integer" }
  }},
  "events": [
    {
      "subject": "crm.opportunity.won.v1",
      "payload": {
        "type": "object",
        "required": ["opportunity_id", "owner_id"],
        "properties": {
          "opportunity_id": { "type": "string" },
          "owner_id": { "type": "string" },
          "items": {
            "type": "array",
            "items": { "type": "object", "properties": {
              "product_id": { "type": "string" },
              "quantity": { "type": "string" }
            }}
          }
        }
      }
    },
    { "subject": "crm.opportunity.lost.v1", "payload": { "type": "object", "properties": {
      "opportunity_id": { "type": "string" }
    }}}
  ]
}`

func TestEventsSignatures_追加字段和新事件不算破坏(t *testing.T) {
	oldSigs, err := eventSignatures([]byte(eventsBase))
	if err != nil {
		t.Fatal(err)
	}
	// 加一个可选字段、加一个全新事件——都是"增"
	added := `{
  "envelope": { "properties": {
    "subject": { "type": "string" },
    "version": { "type": "integer" }
  }},
  "events": [
    {
      "subject": "crm.opportunity.won.v1",
      "payload": { "type": "object",
        "required": ["opportunity_id", "owner_id"],
        "properties": {
          "opportunity_id": { "type": "string" },
          "owner_id": { "type": "string" },
          "currency": { "type": "string" },
          "items": { "type": "array", "items": { "type": "object", "properties": {
            "product_id": { "type": "string" },
            "quantity": { "type": "string" }
          }}}
        }
      }
    },
    { "subject": "crm.opportunity.lost.v1", "payload": { "type": "object", "properties": {
      "opportunity_id": { "type": "string" }
    }}},
    { "subject": "crm.opportunity.stage_changed.v1", "payload": { "type": "object", "properties": {
      "from_stage": { "type": "string" }
    }}}
  ]
}`
	newSigs, err := eventSignatures([]byte(added))
	if err != nil {
		t.Fatal(err)
	}
	if missing := missingSignatures(oldSigs, newSigs); len(missing) != 0 {
		t.Fatalf("纯追加不该有任何丢失签名，得到：%v", missing)
	}
}

func TestEventsSignatures_删字段改类型删subject都被抓到(t *testing.T) {
	oldSigs, _ := eventSignatures([]byte(eventsBase))

	broken := `{
  "envelope": { "properties": {
    "subject": { "type": "string" },
    "version": { "type": "string" }
  }},
  "events": [
    {
      "subject": "crm.opportunity.won.v1",
      "payload": { "type": "object", "properties": {
        "opportunity_id": { "type": "string" },
        "items": { "type": "array", "items": { "type": "object", "properties": {
          "product_id": { "type": "string" }
        }}}
      }}
    }
  ]
}`
	newSigs, _ := eventSignatures([]byte(broken))
	missing := missingSignatures(oldSigs, newSigs)

	want := map[string]bool{
		"envelope.version::type=integer":                                     true, // 改类型 integer→string
		"event::crm.opportunity.lost.v1":                                     true, // 删 subject
		"event.crm.opportunity.lost.v1.payload::type=object":                 true,
		"event.crm.opportunity.lost.v1.payload.opportunity_id::type=string":  true,
		"event.crm.opportunity.won.v1.payload.owner_id::type=string":         true, // 删字段
		"event.crm.opportunity.won.v1.payload.items[].quantity::type=string": true, // 删嵌套字段
	}
	if len(missing) != len(want) {
		t.Fatalf("期望 %d 条丢失签名，得到 %d：%v", len(want), len(missing), missing)
	}
	for _, m := range missing {
		if !want[m] {
			t.Errorf("意外的丢失签名：%s", m)
		}
	}
}

// 真起一个 git 仓库，把 base 版本提交进 main，工作区改坏，跑整个 gate。
func TestEventsBreakingScan_对着真实git仓库跑(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("没有 git")
	}
	root := t.TempDir()
	compDir := filepath.Join(root, "components/crm/opportunity")
	evPath := filepath.Join(compDir, "contracts/events/opportunity.events.json")
	write(t, evPath, eventsBase)

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", compDir}, args...)...)
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("add", ".")
	run("commit", "-qm", "base")

	// 工作区把 owner_id 删了 + lost 事件整个删了
	write(t, evPath, `{
  "envelope": { "properties": { "subject": { "type": "string" }, "version": { "type": "integer" } }},
  "events": [
    { "subject": "crm.opportunity.won.v1", "payload": { "type": "object", "properties": {
      "opportunity_id": { "type": "string" },
      "items": { "type": "array", "items": { "type": "object", "properties": {
        "product_id": { "type": "string" }, "quantity": { "type": "string" }
      }}}
    }}}
  ]
}`)

	violations, err := EventsBreakingScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) == 0 {
		t.Fatal("删了 owner_id 和整个 lost 事件，应该报违规")
	}
	var sawOwner, sawLost bool
	for _, v := range violations {
		if v.Component != "crm/opportunity" {
			t.Errorf("组件 ID 不对：%s", v.Component)
		}
		if v.Missing == "event.crm.opportunity.won.v1.payload.owner_id::type=string" {
			sawOwner = true
		}
		if v.Missing == "event::crm.opportunity.lost.v1" {
			sawLost = true
		}
	}
	if !sawOwner || !sawLost {
		t.Fatalf("没抓全：sawOwner=%v sawLost=%v，全部违规=%+v", sawOwner, sawLost, violations)
	}
}

func TestEventsBreakingScan_新文件没有main基线时跳过(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("没有 git")
	}
	root := t.TempDir()
	compDir := filepath.Join(root, "components/crm/opportunity")
	write(t, filepath.Join(compDir, "contracts/events/opportunity.events.json"), eventsBase)

	cmd := exec.Command("git", "-C", compDir, "init", "-q", "-b", "main")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	// 从没 commit——main 上没有这个文件

	violations, err := EventsBreakingScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("没有 main 基线应该跳过，得到 %v", violations)
	}
}
