package authzeval

import (
	"encoding/json"
	"io/fs"
	"reflect"
	"testing"

	authzcontract "github.com/brickKit/contract-infra-authz/v2"
)

type vector struct {
	ID    string `json:"id"`
	Input struct {
		Bundle       json.RawMessage `json:"bundle"`
		Claims       Claims          `json:"claims"`
		Now          int64           `json:"now"`
		Key          string          `json:"key"`
		ResourceType *ResourceType   `json:"resource_type"`
		Row          *Row            `json:"row"`
		ACL          []ACL           `json:"acl"`
		GraphIDs     []string        `json:"graph_ids"`
	} `json:"input"`
	Expected map[string]json.RawMessage `json:"expected"`
}

// Every decision vector of contract-infra-authz (the pinned tag) is computed exactly, except
// explain (E12), which this evaluator does not produce.
func TestDecisionVectors(t *testing.T) {
	files, err := fs.Glob(authzcontract.FS, "vectors/decision/*.json")
	if err != nil || len(files) < 60 {
		t.Fatalf("vectors: %v (%d files)", err, len(files))
	}
	for _, f := range files {
		b, _ := fs.ReadFile(authzcontract.FS, f)
		var v vector
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		t.Run(v.ID, func(t *testing.T) { checkVector(t, v) })
	}
}

func checkVector(t *testing.T, v vector) {
	bundle, accepted := ParseBundle(v.Input.Bundle)
	want := func(k string, got any) {
		t.Helper()
		raw, ok := v.Expected[k]
		if !ok {
			return
		}
		g, _ := json.Marshal(got)
		var a, b any
		_ = json.Unmarshal(raw, &a)
		_ = json.Unmarshal(g, &b)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%s = %s, want %s", k, g, raw)
		}
	}
	want("bundle", map[bool]string{true: "accepted", false: "refused"}[accepted])
	if !accepted {
		return
	}
	want("token", TokenCheck(bundle, v.Input.Claims))
	if _, ok := v.Expected["has_key"]; !ok {
		return
	}
	e := New(bundle, v.Input.Claims, v.Input.Now, v.Input.ResourceType)
	want("has_key", e.Has(v.Input.Key))
	want("level", e.Level(v.Input.Key))
	want("scope_params", e.Params(v.Input.Key, v.Input.GraphIDs))
	want("degraded", e.Degraded())
	if v.Input.ResourceType != nil && len(v.Input.ResourceType.Fields) > 0 {
		m, ro := e.Fields()
		want("fields", map[string][]string{"masked": m, "read_only": ro})
	}
	if v.Input.Row != nil {
		want("decision", e.Decide(v.Input.Key, *v.Input.Row, v.Input.ACL, v.Input.GraphIDs))
	}
}
