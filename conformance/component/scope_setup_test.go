package compconf

import (
	"testing"

	"github.com/brickKit/be-acceptance/conformance/component/fakes"
)

func planned(t *testing.T, c *Component) *Run {
	t.Helper()
	r := &Run{comp: c, authz: fakes.NewAuthz(), ran: []string{"scope"}}
	r.setupPersonas()
	if r.scope == nil {
		t.Fatal("no scope world")
	}
	return r
}

func TestPlanScopeWidget(t *testing.T) {
	r := planned(t, widget(t))
	w := r.scope
	if w.table != "widgets" || w.viewKey != "conformance.widget.view" || w.res != "conformance.widget.widget" || !w.withChecks {
		t.Fatalf("world = %+v", w)
	}
	if w.cmd == nil || w.cmd.Key != "conformance.widget.approve" || len(w.resourceDims()) != 1 || w.resourceDims()[0] != "region" {
		t.Fatalf("cmd %v dims %v", w.cmd, w.resourceDims())
	}
	for _, p := range []string{"cc_lvl_own", "cc_lvl_all", "cc_mixed", "cc_viewonly", "cc_east", "cc_nodept", "cc_maskme", "cc_rand5"} {
		if _, ok := r.personas[p]; !ok {
			t.Errorf("persona %s missing", p)
		}
	}
	if d := *r.personas["cc_nodept"].Dept; d != "" {
		t.Errorf("cc_nodept dept %q", d)
	}
}

func TestPlanScopeEvaluatesTheMatrix(t *testing.T) {
	r := planned(t, widget(t))
	r.scope.rows = nil
	own := r.personas[scopePersona("own")].Sub
	for _, f := range [][3]string{{own, "/1/7/", "cc-west"}, {"other", "/1/3/", "cc-east"}, {"other", "/1/3/5/", "cc-east"}, {"other", "/1/7/", "cc-east"}} {
		r.scope.rows = append(r.scope.rows, rowOf(f))
	}
	counts := map[string]int{}
	for _, l := range scopeLevels {
		counts[l] = len(r.expect(scopePersona(l), r.scope.viewKey))
	}
	if counts["own"] != 1 || counts["dept"] != 1 || counts["subtree"] != 2 || counts["all"] != 4 { // the own row belongs to cc_lvl_own only
		t.Fatalf("visible counts = %v", counts)
	}
	if n := len(r.expect("cc_east", r.scope.viewKey)); n != 3 {
		t.Fatalf("east sees %d, want 3", n)
	}
	if n := len(r.expect("cc_novalue", r.scope.viewKey)); n != 0 {
		t.Fatalf("no value sees %d", n)
	}
}
