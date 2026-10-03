package compconf

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/brickKit/be-acceptance/conformance/component/authzeval"
	"github.com/brickKit/be-acceptance/conformance/component/fakes"
)

// The scope profile, single records and the resource contract (P6.6, P6.7, P6.8, P6.10–P6.12).

// CP-SCOPE-07: a record the caller cannot see answers 404 NOT_FOUND to a read; one it can see
// answers 200 (P6.6, P3.15).
func caseScope07(ctx context.Context, r *Run) {
	const id = "CP-SCOPE-07"
	if !r.needScope(id) {
		return
	}
	if r.scope.get.Path == "" {
		r.ev.notApplicable(id, "the fixtures resource has no get")
		return
	}
	p := scopePersona("own")
	vis := r.expect(p, r.scope.viewKey)
	for _, row := range r.scope.rows {
		x := r.invoke(ctx, r.scope.get, row.ID, p, nil)
		if vis[row.ID] {
			r.ev.check(id, x.Status == 200, "a visible record read = %d %s", x.Status, x.reason())
		} else {
			r.ev.check(id, x.Status == 404 && x.reason() == "NOT_FOUND", "an invisible record read = %d %s, want 404 NOT_FOUND (P6.6)", x.Status, x.reason())
		}
	}
}

// CP-SCOPE-08: a command on a record the caller cannot see answers 404 NOT_FOUND, like a record
// that does not exist (P6.6, P3.15).
func caseScope08(ctx context.Context, r *Run) {
	const id = "CP-SCOPE-08"
	if !r.needScope(id) {
		return
	}
	w := r.scope
	if w.cmd == nil {
		r.ev.notApplicable(id, "the fixtures resource has no command on one record")
		return
	}
	p := scopePersona("own")
	vis := r.expect(p, w.viewKey)
	n := 0
	for _, row := range w.rows {
		if vis[row.ID] || n >= 4 {
			continue
		}
		n++
		x := r.invoke(ctx, *w.cmd, row.ID, p, nil)
		r.ev.check(id, x.Status == 404 && x.reason() == "NOT_FOUND", "%s on an invisible record = %d %s, want 404 NOT_FOUND (P6.6)", w.cmdName, x.Status, x.reason())
	}
	x := r.invoke(ctx, *w.cmd, uuidv7(), p, nil)
	r.ev.check(id, x.Status == 404 && x.reason() == "NOT_FOUND", "%s on a record that does not exist = %d %s, want 404 NOT_FOUND", w.cmdName, x.Status, x.reason())
}

// CP-SCOPE-09: visible but the command's key missing answers 403 MISSING_PERMISSION; a filter
// value outside the caller's dimension values answers 403 OUT_OF_SCOPE (P6.6). The
// OUT_OF_SCOPE of a command is CP-SCOPE-02.
func caseScope09(ctx context.Context, r *Run) {
	const id = "CP-SCOPE-09"
	if !r.needScope(id) {
		return
	}
	w := r.scope
	n := 0
	if w.cmd != nil {
		n++
		x := r.invoke(ctx, *w.cmd, w.rows[0].ID, "cc_viewonly", nil)
		r.ev.check(id, x.Status == 403 && x.reason() == "MISSING_PERMISSION" && x.metadata("permission") == w.cmd.Key,
			"%s on a visible record without %s = %d %s, want 403 MISSING_PERMISSION (P6.6)", w.cmdName, w.cmd.Key, x.Status, x.reason())
	}
	for param := range w.list.Filters {
		n++
		_, bad := r.listed(ctx, "cc_east", map[string]string{param: "cc-west"})
		r.ev.check(id, bad != nil && bad.Status == 403 && bad.reason() == "OUT_OF_SCOPE",
			"list ?%s=cc-west by a caller holding only cc-east = %d %s, want 403 OUT_OF_SCOPE (P6.6)", param, statusOf(bad), reasonOf(bad))
	}
	if n == 0 {
		r.ev.notApplicable(id, "no command on one record and no dimension filter in the fixtures")
	}
}

// CP-SCOPE-10: List/Can consistency under random grants: for every random persona a record is
// in the list exactly when the single-record decision says visible (_authz/check when the
// resource contract is mounted, else the read), and both agree with the evaluator (P6.5, P6.7).
func caseScope10(ctx context.Context, r *Run) {
	const id = "CP-SCOPE-10"
	if !r.needScope(id) {
		return
	}
	w := r.scope
	for _, p := range w.random {
		listed, bad := r.listed(ctx, p, nil)
		if bad != nil {
			r.ev.check(id, bad.Status == 403 && bad.reason() == "MISSING_PERMISSION", "%s %v list = %d %s", p, r.personas[p].Roles, bad.Status, bad.reason())
			continue
		}
		can := r.canSee(ctx, p)
		for _, row := range w.rows {
			r.ev.check(id, listed[row.ID] == can[row.ID], "%s %v: record in list %v, single decision visible %v (P6.7)", p, r.personas[p].Roles, listed[row.ID], can[row.ID])
		}
		if d := diffSets(r.expect(p, w.viewKey), listed); d != "" {
			r.ev.fail(id, "%s %v dept %q: %s against the reference evaluation (P6.5)", p, r.personas[p].Roles, deref(r.personas[p].Dept), d)
		}
	}
	r.ev.pass(id, fmt.Sprintf("seed %d", w.seed))
}

func deref(s *string) string {
	if s == nil {
		return "<absent>"
	}
	return *s
}

// canSee is the single-record visibility of every world record for persona p.
func (r *Run) canSee(ctx context.Context, p string) map[string]bool {
	w := r.scope
	out := map[string]bool{}
	if w.withChecks {
		res, _ := r.authzCheck(ctx, p, w.viewKey, w.rows)
		for i, row := range w.rows {
			if i < len(res) {
				out[row.ID] = res[i].Visible
			}
		}
		return out
	}
	for _, row := range w.rows {
		out[row.ID] = r.invoke(ctx, w.get, row.ID, p, nil).Status == 200
	}
	return out
}

func (r *Run) contractPath(tail string) string { return "/" + r.comp.ID() + tail }

// authzCheck posts _authz/check for rows as persona p.
func (r *Run) authzCheck(ctx context.Context, p, key string, rows []authzeval.Row) ([]authzeval.Decision, *exchange) {
	var checks []map[string]string
	for _, row := range rows {
		checks = append(checks, map[string]string{"key": key, "type": r.scope.rtype.Type, "id": row.ID})
	}
	x := r.call(ctx, "POST", r.contractPath("/_authz/check"), withToken(r.token(p)), withBody([]byte(mustJSON(map[string]any{"checks": checks}))))
	var out struct {
		Results []authzeval.Decision `json:"results"`
	}
	_ = json.Unmarshal(x.Body, &out)
	return out.Results, x
}

// CP-SCOPE-11: fields of a field set the caller may not read are null and listed in _masked;
// sorting by one answers 400 SORT_FORBIDDEN (P6.8).
func caseScope11(ctx context.Context, r *Run) {
	const id = "CP-SCOPE-11"
	if !r.needScope(id) {
		return
	}
	w := r.scope
	if len(w.rtype.Fields) == 0 {
		r.ev.notApplicable(id, "the resource declares no field set")
		return
	}
	f := w.rtype.Fields[0]
	persona := "cc_maskme" // registered at setup (scopeRoles)
	x := r.invoke(ctx, w.list, "", persona, nil)
	if !r.ev.check(id, x.Status == 200, "list without %s = %d %s", f.Read, x.Status, x.reason()) {
		return
	}
	for _, it := range listItems(x.Body) {
		masked, _ := it["_masked"].([]any)
		for _, c := range f.Columns {
			r.ev.check(id, it[c] == nil && containsAny(masked, c), "field %s without %s = %v, _masked %v: want null and listed (P6.8)", c, f.Read, it[c], masked)
		}
		break
	}
	if s := w.list.Sort; s != nil && len(s.Masked) > 0 {
		y := r.invoke(ctx, w.list, "", persona, map[string]string{s.Param: s.Masked[0]})
		r.ev.check(id, y.Status == 400 && y.reason() == "SORT_FORBIDDEN", "sort=%s without %s = %d %s, want 400 SORT_FORBIDDEN (P6.8)", s.Masked[0], f.Read, y.Status, y.reason())
	}
}

func (r *Run) needContract(id string) bool {
	if !r.needScope(id) {
		return false
	}
	if !r.scope.withChecks {
		r.ev.notApplicable(id, "assembly.yaml declares no resources: the resource contract is not mounted (P6.10)")
		return false
	}
	return true
}

// CP-SCOPE-12: _authz/check answers one decision per check, in order, equal to the evaluator,
// and refuses 501 checks with 400 BATCH_TOO_LARGE (P6.10, E10).
func caseScope12(ctx context.Context, r *Run) {
	const id = "CP-SCOPE-12"
	if !r.needContract(id) {
		return
	}
	w := r.scope
	for _, l := range []string{"own", "subtree"} {
		p := scopePersona(l)
		res, x := r.authzCheck(ctx, p, w.viewKey, w.rows)
		if !r.ev.check(id, x.Status == 200 && len(res) == len(w.rows), "_authz/check of %d rows = %d %s with %d results", len(w.rows), x.Status, x.reason(), len(res)) {
			continue
		}
		want := r.expect(p, w.viewKey)
		for i, row := range w.rows {
			d := res[i]
			ok := d.Visible == want[row.ID] && d.Allowed == want[row.ID] && (d.Reason == "") == want[row.ID]
			if !want[row.ID] {
				ok = ok && d.Reason == "NOT_FOUND"
			}
			r.ev.check(id, ok, "%s check %d = %+v, want visible %v (E10)", p, i, d, want[row.ID])
		}
	}
	many := make([]authzeval.Row, 501)
	for i := range many {
		many[i] = w.rows[0]
	}
	_, x := r.authzCheck(ctx, scopePersona("all"), w.viewKey, many)
	r.ev.check(id, x.Status == 400 && x.reason() == "BATCH_TOO_LARGE", "_authz/check with 501 checks = %d %s, want 400 BATCH_TOO_LARGE (P6.10)", x.Status, x.reason())
}

// CP-SCOPE-13: explain for a record the caller cannot see says not_visible and reveals none of
// its facts (owner, department, dimension values) (P6.10, E12 R62).
func caseScope13(ctx context.Context, r *Run) {
	const id = "CP-SCOPE-13"
	if !r.needContract(id) {
		return
	}
	w := r.scope
	p := scopePersona("own")
	vis := r.expect(p, w.viewKey)
	for _, row := range w.rows {
		if vis[row.ID] {
			continue
		}
		t := fmt.Sprintf("%s?key=%s&type=%s&id=%s", r.contractPath("/_authz/explain"), w.viewKey, w.rtype.Type, row.ID)
		x := r.call(ctx, "GET", t, withToken(r.token(p)))
		if !r.ev.check(id, x.Status == 200, "explain of an invisible record = %d %s", x.Status, x.reason()) {
			return
		}
		r.ev.check(id, jsonPathString(x.Body, "$.decision") == "not_visible", "explain decision %q, want not_visible", jsonPathString(x.Body, "$.decision"))
		body := string(x.Body)
		leaks := []string{row.Owner, row.DeptPath}
		for _, v := range row.Values {
			leaks = append(leaks, v)
		}
		for _, l := range leaks {
			r.ev.check(id, l == "" || !strings.Contains(body, l), "explain of an invisible record reveals %q (E12 R62)", l)
		}
		return
	}
	r.ev.fail(id, "no invisible record to explain")
}

// CP-SCOPE-14: with the provider's sharing false, every _shares endpoint answers 501
// CAPABILITY_UNAVAILABLE with metadata.capability sharing (P6.10, E12).
func caseScope14(ctx context.Context, r *Run) {
	const id = "CP-SCOPE-14"
	if !r.needContract(id) {
		return
	}
	w := r.scope
	base := r.contractPath(fmt.Sprintf("/_shares/%s/%s", w.rtype.Type, w.rows[0].ID))
	for _, c := range []struct{ m, t string }{{"GET", base}, {"POST", base}, {"DELETE", base + "/" + uuidv7()}} {
		opts := []reqOpt{withToken(r.token(pAll))}
		if c.m == "POST" {
			opts = append(opts, withBody([]byte(`{"relation":"viewer","subject":"role:compconf"}`)))
		}
		x := r.call(ctx, c.m, c.t, opts...)
		r.ev.check(id, x.Status == 501 && x.reason() == "CAPABILITY_UNAVAILABLE" && x.metadata("capability") == "sharing",
			"%s _shares with sharing=false = %d %s capability=%q, want 501 CAPABILITY_UNAVAILABLE sharing (P6.10)", c.m, x.Status, x.reason(), x.metadata("capability"))
	}
}

// CP-SCOPE-15: a share arriving by the changefeed is visible within 6 s, at once with
// X-Authz-Revision, and gone after its deletion (P6.11, P6.12).
func caseScope15(ctx context.Context, r *Run) {
	const id = "CP-SCOPE-15"
	if !r.needContract(id) {
		return
	}
	w := r.scope
	var rel string
	for n, x := range w.rtype.Relations {
		for _, k := range x.Grants {
			if k == w.viewKey && x.OwnedBy != "component" {
				rel = n
			}
		}
	}
	if rel == "" {
		r.ev.notApplicable(id, "no shareable relation grants the view key")
		return
	}
	r.authz.SetCapability("sharing", true)
	defer r.authz.SetCapability("sharing", false)
	p := scopePersona("own")
	vis := r.expect(p, w.viewKey)
	var row authzeval.Row
	for _, x := range w.rows {
		if !vis[x.ID] {
			row = x
		}
	}
	t := fakes.TupleRec{Type: w.rtype.Type, ID: row.ID, Relation: rel, Subject: "user:" + r.personas[p].Sub}
	rev := r.authz.PutTuple(t)
	x := r.invoke(ctx, w.get, row.ID, p, nil, withHeader("X-Authz-Revision", rev))
	r.ev.check(id, x.Status == 200, "read with X-Authz-Revision %s right after the share = %d %s, want 200 (P6.11)", rev, x.Status, x.reason())
	r.ev.check(id, r.waitListed(ctx, p, row.ID, true, 6*time.Second), "the shared record is not in the list within 6 s (P6.12)")
	r.authz.DeleteTuple(t)
	r.ev.check(id, r.waitListed(ctx, p, row.ID, false, 6*time.Second), "the record is still in the list 6 s after the share was revoked (P6.12)")
}

func (r *Run) waitListed(ctx context.Context, p, rid string, want bool, d time.Duration) bool {
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(500 * time.Millisecond) {
		if got, _ := r.listed(ctx, p, nil); got[rid] == want {
			return true
		}
	}
	return false
}
