package compconf

import (
	"context"
	"encoding/json"
	"fmt"
)

// Making the component do things through its fixtures operations.

// opPersona is the persona an operation runs as: its `as`, else one holding its key, else the
// suite's all-keys persona.
func (r *Run) opPersona(op FixtureOp) string {
	if op.As != "" {
		return op.As
	}
	if op.Key != "" {
		return r.personaWith(op.Key)
	}
	return pAll
}

// invoke runs a fixtures REST operation with {id} = id as persona ("" = opPersona), with the
// operation's body; query overrides individual query parameters.
func (r *Run) invoke(ctx context.Context, op FixtureOp, id, persona string, query map[string]string, opts ...reqOpt) *exchange {
	return r.invokeAt(ctx, r.base, op, id, persona, query, opts...)
}

func (r *Run) invokeAt(ctx context.Context, base string, op FixtureOp, id, persona string, query map[string]string, opts ...reqOpt) *exchange {
	if persona == "" {
		persona = r.opPersona(op)
	}
	all := []reqOpt{withToken(r.token(persona))}
	if b := r.fixtureBodyFor(op, id); b != nil {
		all = append(all, withBody(b))
	} else if op.Method == "POST" || op.Method == "PUT" || op.Method == "PATCH" {
		all = append(all, withBody([]byte("{}")))
	}
	return r.callAt(ctx, base, op.Method, r.fixtureTarget(op, id, query), append(all, opts...)...)
}

// creatable lists the resources whose fixtures have a REST create, sorted.
func (r *Run) creatable() []string {
	var out []string
	for _, res := range sortedKeys(r.comp.Fixtures.Resources) {
		if c, ok := r.comp.Fixtures.Resources[res]["create"]; ok && c.Path != "" {
			out = append(out, res)
		}
	}
	return out
}

// create makes one record of res as persona ("" = the create's persona) and returns its ID.
func (r *Run) create(ctx context.Context, res, persona string, opts ...reqOpt) (string, *exchange, error) {
	op, ok := r.comp.Fixtures.Resources[res]["create"]
	if !ok {
		return "", nil, fmt.Errorf("resource %s has no create in the fixtures", res)
	}
	x := r.invoke(ctx, op, "", persona, nil, opts...)
	if x.Err != nil || x.Status >= 300 {
		return "", x, fmt.Errorf("create %s: %d %s %v", res, x.Status, x.reason(), x.Err)
	}
	path := op.ID
	if path == "" {
		path = "$.id"
	}
	id := jsonPathString(x.Body, path)
	if id == "" {
		return "", x, fmt.Errorf("create %s answered %d but %s is empty in %.200s", res, x.Status, path, x.Body)
	}
	return id, x, nil
}

// createAny creates a record of the first creatable resource.
func (r *Run) createAny(ctx context.Context) (string, string, error) {
	res := r.creatable()
	if len(res) == 0 {
		return "", "", fmt.Errorf("the fixtures have no REST create")
	}
	id, _, err := r.create(ctx, res[0], "")
	return res[0], id, err
}

// listItems returns the objects of a list answer: the array field named items, else the first
// top-level array of objects (P3.8: the field name is the contract's).
func listItems(body []byte) []map[string]any {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil
	}
	pick := func(v any) []map[string]any {
		arr, ok := v.([]any)
		if !ok {
			return nil
		}
		out := []map[string]any{}
		for _, e := range arr {
			if o, ok := e.(map[string]any); ok {
				out = append(out, o)
			}
		}
		return out
	}
	if v, ok := m["items"]; ok {
		return pick(v)
	}
	for _, k := range sortedKeys(m) {
		if items := pick(m[k]); len(items) > 0 {
			return items
		}
	}
	return nil
}
