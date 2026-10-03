package compconf

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// The idempotency profile (P13, P3.7): every fixtures REST operation marked idempotent.

// idemOp is an idempotent fixtures operation with its resource and name.
type idemOp struct {
	res, name string
	op        FixtureOp
}

func (r *Run) idemOps() []idemOp {
	var out []idemOp
	for _, res := range sortedKeys(r.comp.Fixtures.Resources) {
		for _, name := range sortedKeys(r.comp.Fixtures.Resources[res]) {
			if op := r.comp.Fixtures.Resources[res][name]; op.Idempotent && op.Path != "" {
				out = append(out, idemOp{res, name, op})
			}
		}
	}
	return out
}

func newKey() string { return "compconf-" + uuidv7() }

// target prepares the record an operation acts on ("" for a create).
func (r *Run) idemTarget(ctx context.Context, o idemOp) (string, error) {
	if o.name == "create" {
		return "", nil
	}
	id, _, err := r.create(ctx, o.res, "")
	return id, err
}

// invokeKey runs the operation with the key in the header, as persona ("" = its own).
func (r *Run) invokeKey(ctx context.Context, o idemOp, id, persona, key string, opts ...reqOpt) *exchange {
	return r.invoke(ctx, o.op, id, persona, nil, append([]reqOpt{withHeader("Idempotency-Key", key)}, opts...)...)
}

// sameAnswer: same status and the same JSON body (a replay returns the stored result).
func sameAnswer(a, b *exchange) bool {
	if a.Status != b.Status {
		return false
	}
	var x, y any
	if json.Unmarshal(a.Body, &x) != nil || json.Unmarshal(b.Body, &y) != nil {
		return string(a.Body) == string(b.Body)
	}
	return mustJSON(x) == mustJSON(y)
}

func (r *Run) needIdem(id string) []idemOp {
	ops := r.idemOps()
	if len(ops) == 0 {
		r.ev.notApplicable(id, "no fixtures REST operation is marked idempotent")
		return nil
	}
	if !r.needMain(id) {
		return nil
	}
	return ops
}

// CP-IDEM-01: the same key again replays the first answer (status and body) and does not
// execute again (P13.1, P13.3).
func caseIdem01(ctx context.Context, r *Run) {
	const id = "CP-IDEM-01"
	for _, o := range r.needIdem(id) {
		tid, err := r.idemTarget(ctx, o)
		if !r.ev.require(id, err == nil, "preparing %s: %v", o.res, err) {
			continue
		}
		key := newKey()
		first := r.invokeKey(ctx, o, tid, "", key)
		if !r.ev.check(id, first.Status < 300, "%s.%s with a new key = %d %s", o.res, o.name, first.Status, first.reason()) {
			continue
		}
		again := r.invokeKey(ctx, o, tid, "", key)
		r.ev.check(id, sameAnswer(first, again), "%s.%s replayed = %d %.200s, first answer %d %.200s (P13.3)", o.res, o.name, again.Status, again.Body, first.Status, first.Body)
	}
}

// mutateFingerprint changes one fingerprint field of the body ("" when none can change).
func mutateFingerprint(body []byte, fields []string) ([]byte, string) {
	var m map[string]any
	if json.Unmarshal(body, &m) != nil {
		return nil, ""
	}
	cands := fields
	if cands == nil { // not declared: any top-level string field
		for k, v := range m {
			if _, ok := v.(string); ok && k != "idempotency_key" {
				cands = append(cands, k)
			}
		}
		sort.Strings(cands)
	}
	for _, f := range cands {
		if s, ok := m[f].(string); ok {
			m[f] = s + "-compconf"
			b, _ := json.Marshal(m)
			return b, f
		}
	}
	return nil, ""
}

// CP-IDEM-02: the same key with another fingerprint answers 400 IDEMPOTENCY_MISMATCH (P13.2,
// P13.5).
func caseIdem02(ctx context.Context, r *Run) {
	const id = "CP-IDEM-02"
	n := 0
	for _, o := range r.needIdem(id) {
		body := r.fixtureBody(o.op)
		other, field := mutateFingerprint(body, o.op.Fingerprint)
		if other == nil {
			continue
		}
		tid, err := r.idemTarget(ctx, o)
		if !r.ev.require(id, err == nil, "preparing %s: %v", o.res, err) {
			continue
		}
		n++
		key := newKey()
		r.invokeKey(ctx, o, tid, "", key)
		x := r.invokeKey(ctx, o, tid, "", key, withBody(other))
		r.ev.check(id, x.Status == 400 && x.reason() == "IDEMPOTENCY_MISMATCH",
			"%s.%s with the same key and %s changed = %d %s, want 400 IDEMPOTENCY_MISMATCH (P13.2)", o.res, o.name, field, x.Status, x.reason())
	}
	if n == 0 {
		r.ev.notApplicable(id, "no idempotent operation has a fingerprint field the suite can change")
	}
}

// CP-IDEM-03: the same key on another record answers IDEMPOTENCY_MISMATCH (P13.2).
func caseIdem03(ctx context.Context, r *Run) {
	const id = "CP-IDEM-03"
	n := 0
	for _, o := range r.needIdem(id) {
		if o.name == "create" {
			continue
		}
		a, errA := r.idemTarget(ctx, o)
		b, errB := r.idemTarget(ctx, o)
		if !r.ev.require(id, errA == nil && errB == nil, "preparing two %s: %v %v", o.res, errA, errB) {
			continue
		}
		n++
		key := newKey()
		r.invokeKey(ctx, o, a, "", key)
		x := r.invokeKey(ctx, o, b, "", key)
		r.ev.check(id, x.Status == 400 && x.reason() == "IDEMPOTENCY_MISMATCH",
			"%s.%s on another record with the same key = %d %s, want 400 IDEMPOTENCY_MISMATCH (P13.2)", o.res, o.name, x.Status, x.reason())
	}
	if n == 0 {
		r.ev.notApplicable(id, "no idempotent operation acts on an existing record")
	}
}

// CP-IDEM-04: the same key used for another command answers IDEMPOTENCY_MISMATCH (P13.2).
func caseIdem04(ctx context.Context, r *Run) {
	const id = "CP-IDEM-04"
	ops := r.needIdem(id)
	var a, b *idemOp
	for i := range ops {
		for j := range ops {
			if a == nil && ops[i].op.Key != ops[j].op.Key && ops[i].res == ops[j].res {
				a, b = &ops[i], &ops[j]
			}
		}
	}
	if a == nil {
		if ops != nil {
			r.ev.notApplicable(id, "no two idempotent operations of one resource with different permission keys")
		}
		return
	}
	persona := pAll // one caller allowed both commands
	ta, errA := r.idemTarget(ctx, *a)
	tb, errB := r.idemTarget(ctx, *b)
	if !r.ev.require(id, errA == nil && errB == nil, "preparing: %v %v", errA, errB) {
		return
	}
	key := newKey()
	first := r.invokeKey(ctx, *a, ta, persona, key)
	r.ev.require(id, first.Status < 300, "%s.%s = %d %s", a.res, a.name, first.Status, first.reason())
	x := r.invokeKey(ctx, *b, tb, persona, key)
	r.ev.check(id, x.Status == 400 && x.reason() == "IDEMPOTENCY_MISMATCH",
		"%s.%s with the key of %s.%s = %d %s, want 400 IDEMPOTENCY_MISMATCH (P13.2)", b.res, b.name, a.res, a.name, x.Status, x.reason())
}

// CP-IDEM-06: two callers with one key are independent: each executes, neither sees the other's
// result (P13.1, P13.4).
func caseIdem06(ctx context.Context, r *Run) {
	const id = "CP-IDEM-06"
	for _, o := range r.needIdem(id) {
		if o.name != "create" {
			continue
		}
		key := newKey()
		a := r.invokeKey(ctx, o, "", pAll, key)
		b := r.invokeKey(ctx, o, "", pAllTwin, key)
		ida, idb := jsonPathString(a.Body, idPath(o.op)), jsonPathString(b.Body, idPath(o.op))
		r.ev.check(id, a.Status < 300 && b.Status < 300 && ida != "" && ida != idb,
			"two callers, one key, %s.create: %d %s and %d %s, want two records (P13.4)", o.res, a.Status, ida, b.Status, idb)
		return
	}
	r.ev.notApplicable(id, "no idempotent create")
}

func idPath(op FixtureOp) string {
	if op.ID != "" {
		return op.ID
	}
	return "$.id"
}

// CP-IDEM-07: ten concurrent requests with one key execute once: every answer is the same
// result or 409 IDEMPOTENCY_IN_PROGRESS (P13.6, P10.9).
func caseIdem07(ctx context.Context, r *Run) {
	const id = "CP-IDEM-07"
	for _, o := range r.needIdem(id) {
		tid, err := r.idemTarget(ctx, o)
		if !r.ev.require(id, err == nil, "preparing: %v", err) {
			continue
		}
		key := newKey()
		var mu sync.Mutex
		var wg sync.WaitGroup
		var got []*exchange
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				x := r.invokeKey(ctx, o, tid, "", key)
				mu.Lock()
				got = append(got, x)
				mu.Unlock()
			}()
		}
		wg.Wait()
		var ok *exchange
		for _, x := range got {
			switch {
			case x.Status == 409 && x.reason() == "IDEMPOTENCY_IN_PROGRESS":
			case x.Status < 300 && ok == nil:
				ok = x
			case x.Status < 300:
				r.ev.check(id, sameAnswer(ok, x), "%s.%s: two concurrent requests with one key answered differently: %.120s / %.120s (P13.6: executed twice)", o.res, o.name, ok.Body, x.Body)
			default:
				r.ev.fail(id, "%s.%s concurrent with one key = %d %s, want the result or 409 IDEMPOTENCY_IN_PROGRESS (P13.6)", o.res, o.name, x.Status, x.reason())
			}
		}
		r.ev.check(id, ok != nil, "%s.%s: none of ten concurrent requests succeeded", o.res, o.name)
	}
}

// CP-IDEM-08: the key in the header and in the body field idempotency_key are equivalent; both
// with different values answer 400 IDEMPOTENCY_MISMATCH (P3.7).
func caseIdem08(ctx context.Context, r *Run) {
	const id = "CP-IDEM-08"
	for _, o := range r.needIdem(id) {
		var m map[string]any
		if json.Unmarshal(r.fixtureBody(o.op), &m) != nil {
			m = map[string]any{}
		}
		tid, err := r.idemTarget(ctx, o)
		if !r.ev.require(id, err == nil, "preparing: %v", err) {
			continue
		}
		key := newKey()
		m["idempotency_key"] = key
		inBody, _ := json.Marshal(m)
		first := r.invoke(ctx, o.op, tid, "", nil, withBody(inBody))
		again := r.invokeKey(ctx, o, tid, "", key)
		r.ev.check(id, first.Status < 300 && sameAnswer(first, again),
			"%s.%s: key in the body %d, then the same key in the header %d %.120s: not the same command (P3.7)", o.res, o.name, first.Status, again.Status, again.Body)
		m["idempotency_key"] = newKey()
		both, _ := json.Marshal(m)
		x := r.invokeKey(ctx, o, tid, "", newKey(), withBody(both))
		r.ev.check(id, x.Status == 400 && x.reason() == "IDEMPOTENCY_MISMATCH",
			"%s.%s with different header and body keys = %d %s, want 400 IDEMPOTENCY_MISMATCH (P3.7)", o.res, o.name, x.Status, x.reason())
	}
}

// CP-IDEM-05: a two-step command whose downstream call hangs (fixtures jobs.reconcilers) keeps
// its key claimed: the same key answers 409 IDEMPOTENCY_IN_PROGRESS (P13.3).
func caseIdem05(ctx context.Context, r *Run) {
	const id = "CP-IDEM-05"
	recs := r.comp.Fixtures.Jobs.Reconcilers
	if len(recs) == 0 {
		r.ev.notApplicable(id, "fixtures declare no reconciler to stall a two-step command")
		return
	}
	if !r.needMain(id) {
		return
	}
	rc := recs[0]
	dep, method := splitCall(rc.Stall)
	peer := r.peers[dep]
	if !r.ev.require(id, peer != nil, "no fake peer for %s", dep) {
		return
	}
	res, name := splitVia(rc.Via)
	o := idemOp{res, name, r.comp.Fixtures.Resources[res][name]}
	tid, err := r.idemTarget(ctx, o)
	if !r.ev.require(id, err == nil, "preparing: %v", err) {
		return
	}
	prev := peer.Answer(method)
	hung := prev
	hung.Hang = true
	peer.SetAnswer(method, hung)
	defer func() { peer.SetAnswer(method, prev); peer.Release() }()
	key := newKey()
	first := r.invokeKey(ctx, o, tid, "", key)
	x := r.invokeKey(ctx, o, tid, "", key)
	r.ev.check(id, x.Status == 409 && x.reason() == "IDEMPOTENCY_IN_PROGRESS",
		"%s while the first call (%d) waits for %s = %d %s, want 409 IDEMPOTENCY_IN_PROGRESS (P13.3)", rc.Via, first.Status, method, x.Status, x.reason())
	r.ev.pass(id, fmt.Sprintf("first answer %d", first.Status))
}

// splitCall splits "<dependency ID> <full rpc name>".
func splitCall(s string) (string, string) {
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' {
			return s[:i], s[i+1:]
		}
	}
	return s, ""
}
