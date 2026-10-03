package compconf

import (
	"context"
	"fmt"
	"time"

	"github.com/brickKit/be-acceptance/conformance/component/fakes"
)

// The scope profile, list side (P6.3–P6.7). Records carry facts the suite wrote; the
// reference evaluator says what each persona must see.

func (r *Run) needScope(id string) bool {
	if !r.needMain(id) {
		return false
	}
	if r.scope == nil {
		r.ev.fail(id, "the scope world could not be built (see CP-SCOPE-01)")
		return false
	}
	return true
}

// checkList compares persona p's list with the evaluator.
func (r *Run) checkList(ctx context.Context, id, p string, req string) {
	want := r.expect(p, r.scope.viewKey)
	got, bad := r.listed(ctx, p, nil)
	if !r.ev.check(id, bad == nil, "%s list = %d %s (%s)", p, statusOf(bad), reasonOf(bad), req) {
		return
	}
	d := diffSets(want, got)
	r.ev.check(id, d == "", "%s: %s (%s)", p, d, req)
}

func statusOf(x *exchange) int {
	if x == nil {
		return 0
	}
	return x.Status
}

func reasonOf(x *exchange) string {
	if x == nil {
		return ""
	}
	return x.reason()
}

// CP-SCOPE-01: own / dept / subtree / all, each on the designed records (P6.3, P6.5).
func caseScope01(ctx context.Context, r *Run) {
	const id = "CP-SCOPE-01"
	if !r.needScope(id) {
		return
	}
	for _, l := range scopeLevels {
		r.checkList(ctx, id, scopePersona(l), "level "+l+", P6.3 P6.5")
	}
	r.ev.pass(id, fmt.Sprintf("%d records", len(r.scope.rows)))
}

// CP-SCOPE-02: levels are evaluated per key: view at all and the command at own lets the caller
// see every record but act only on its own (P6.3).
func caseScope02(ctx context.Context, r *Run) {
	const id = "CP-SCOPE-02"
	if !r.needScope(id) {
		return
	}
	w := r.scope
	if w.cmd == nil {
		r.ev.notApplicable(id, "the fixtures resource has no command on one record with its own key")
		return
	}
	r.checkList(ctx, id, "cc_mixed", "view at all, P6.3")
	can := r.expect("cc_mixed", w.cmd.Key)
	for i, row := range w.rows {
		if i%3 != 0 {
			continue // a sample keeps the commands few
		}
		x := r.invoke(ctx, *w.cmd, row.ID, "cc_mixed", nil)
		if can[row.ID] {
			r.ev.check(id, x.Status < 300, "%s on its own record = %d %s (%s at own)", w.cmdName, x.Status, x.reason(), w.cmd.Key)
		} else {
			r.ev.check(id, x.Status == 403 && x.reason() == "OUT_OF_SCOPE", "%s on a visible record outside %s's own level = %d %s, want 403 OUT_OF_SCOPE (P6.3, P6.6)", w.cmdName, w.cmd.Key, x.Status, x.reason())
		}
	}
}

// CP-SCOPE-03: a resource dimension filters by the granted values; * sees every value; no value
// sees nothing (P6.3); a filter parameter narrows within them.
func caseScope03(ctx context.Context, r *Run) {
	const id = "CP-SCOPE-03"
	if !r.needScope(id) {
		return
	}
	dims := r.scope.resourceDims()
	if len(dims) == 0 {
		r.ev.notApplicable(id, "the resource has no dimension besides owner and org")
		return
	}
	r.checkList(ctx, id, "cc_east", "values cc-east, P6.3")
	r.checkList(ctx, id, scopePersona("all"), "values *, P6.3")
	got, bad := r.listed(ctx, "cc_novalue", nil)
	r.ev.check(id, bad == nil && len(got) == 0, "a role with no value of %v sees %d records (%d %s), want none (P6.3)", dims, len(got), statusOf(bad), reasonOf(bad))
	for param, f := range r.scope.list.Filters {
		got, bad := r.listed(ctx, scopePersona("all"), map[string]string{param: "cc-east"})
		ok := bad == nil
		for _, row := range r.scope.rows {
			ok = ok && got[row.ID] == (row.Values[f.Dimension] == "cc-east")
		}
		r.ev.check(id, ok, "list ?%s=cc-east as level all does not return exactly the cc-east records (%d %s)", param, statusOf(bad), reasonOf(bad))
	}
}

// CP-SCOPE-04: a grant's until is compared with the clock at decision time: an expired role
// gives nothing, and a role expiring during the run stops working without a bundle change
// (P6.3).
func caseScope04(ctx context.Context, r *Run) {
	const id = "CP-SCOPE-04"
	if !r.needScope(id) {
		return
	}
	l := r.scope.list
	x := r.invoke(ctx, l, "", "cc_expired", nil)
	r.ev.check(id, x.Status == 403 && x.reason() == "MISSING_PERMISSION", "list with only a role whose until passed = %d %s, want 403 MISSING_PERMISSION (P6.3, E3)", x.Status, x.reason())
	until := time.Now().Add(25 * time.Second).Unix()
	r.authz.SetRole("cc_role_soon", r.scopeKeys(), fakes.RoleGrant{DefaultLevel: "all", Until: &until})
	r.addScopePersona("cc_soon", "/1/3/", "cc_role_soon")
	loaded := false
	for time.Now().Unix() < until-3 && !loaded {
		loaded = r.invoke(ctx, l, "", "cc_soon", nil).Status == 200
		time.Sleep(time.Second)
	}
	if !r.ev.check(id, loaded, "a role valid for 25 s never worked: the bundle was not picked up within its 15 s poll (P6.1)") {
		return
	}
	time.Sleep(time.Until(time.Unix(until+1, 0)))
	x = r.invoke(ctx, l, "", "cc_soon", nil)
	r.ev.check(id, x.Status == 403, "list 1 s after the role's until = %d %s, want 403: until is compared with the local clock (P6.3)", x.Status, x.reason())
}

// CP-SCOPE-05: several roles: the highest level among them wins (P6.3).
func caseScope05(ctx context.Context, r *Run) {
	const id = "CP-SCOPE-05"
	if r.needScope(id) {
		r.checkList(ctx, id, "cc_multi", "roles at own and subtree, P6.3")
	}
}

// CP-SCOPE-06: a caller without a department sees only its own records at any level below
// all (P6.4, R60).
func caseScope06(ctx context.Context, r *Run) {
	const id = "CP-SCOPE-06"
	if !r.needScope(id) {
		return
	}
	r.checkList(ctx, id, "cc_nodept", "no department, level subtree, P6.4")
	got, _ := r.listed(ctx, "cc_nodept", nil)
	me := r.personas["cc_nodept"].Sub
	for _, row := range r.scope.rows {
		if got[row.ID] && row.Owner != me {
			r.ev.fail(id, "a caller without a department sees a record of another owner in %s (R60: empty dept_path is not the root)", row.DeptPath)
			return
		}
	}
}
