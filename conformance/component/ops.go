package compconf

import (
	"strings"
)

// Choosing operations of the component to exercise generic rules.

// probeOp is a key-guarded GET, preferring one without path parameters.
func (r *Run) probeOp() (Operation, bool) {
	var fallback *Operation
	for i, o := range r.comp.Operations {
		if o.KeyGuarded() && o.Method == "GET" {
			if !strings.Contains(o.Path, "{") {
				return o, true
			}
			if fallback == nil {
				fallback = &r.comp.Operations[i]
			}
		}
	}
	if fallback != nil {
		return *fallback, true
	}
	for _, o := range r.comp.Operations {
		if o.Protected() {
			return o, true
		}
	}
	return Operation{}, false
}

// templatedOp is a key-guarded GET with a path parameter.
func (r *Run) templatedOp() (Operation, bool) {
	for _, o := range r.comp.Operations {
		if o.KeyGuarded() && o.Method == "GET" && strings.Contains(o.Path, "{") {
			return o, true
		}
	}
	return Operation{}, false
}

// publicOp is a Public GET.
func (r *Run) publicOp() (Operation, bool) {
	for _, o := range r.comp.Operations {
		if o.Guard == GuardPublic && o.Method == "GET" {
			return o, true
		}
	}
	return Operation{}, false
}

// bodyOp is an operation that takes a body, preferring a protected one.
func (r *Run) bodyOp() (Operation, bool) {
	var pub *Operation
	for i, o := range r.comp.Operations {
		if o.Method == "POST" || o.Method == "PUT" || o.Method == "PATCH" {
			if o.Protected() {
				return o, true
			}
			if pub == nil {
				pub = &r.comp.Operations[i]
			}
		}
	}
	if pub != nil {
		return *pub, true
	}
	return Operation{}, false
}

// target fills the path parameters of an OpenAPI path with fresh UUIDs.
func (r *Run) target(o Operation) string { return r.substitute(o.Path, "") }

// personaFor picks a persona allowed through the operation's guard.
func (r *Run) personaFor(o Operation) string {
	if o.KeyGuarded() {
		return r.personaWith(o.Guard)
	}
	return pAll
}

// fixtureOp resolves "resources.<res>.<op>".
func (r *Run) fixtureOp(via string) (FixtureOp, bool) {
	rest := strings.TrimPrefix(via, "resources.")
	i := strings.LastIndex(rest, ".")
	if i < 0 {
		return FixtureOp{}, false
	}
	op, ok := r.comp.Fixtures.Resources[rest[:i]][rest[i+1:]]
	return op, ok
}

// fixtureBody is the body of a fixtures operation.
func (r *Run) fixtureBody(op FixtureOp) []byte {
	if op.BodyFile != "" {
		if b, err := r.comp.FixtureFile(op.BodyFile); err == nil {
			return []byte(r.substitute(string(b), ""))
		}
	}
	if op.Body != nil {
		return []byte(r.substitute(mustJSON(op.Body), ""))
	}
	return nil
}

// fixtureTarget is the path and query of a fixtures REST operation.
func (r *Run) fixtureTarget(op FixtureOp, id string, query map[string]string) string {
	t := r.substitute(op.Path, id)
	q := []string{}
	for _, k := range sortedKeys(op.Query) {
		v := op.Query[k]
		if qv, ok := query[k]; ok {
			v = qv
		}
		q = append(q, k+"="+r.substitute(v, id))
	}
	if len(q) > 0 {
		t += "?" + strings.Join(q, "&")
	}
	return t
}
