package compconf

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Start-up with dependencies down, readiness and its latch (CP-CORE-03, -04, -05, CP-AUTH-10,
// CP-AUTH-13). The steps run in order and share the main instance.

// stepStartWithDepsDown is a setup step: PostgreSQL, the bus and the authorization provider
// are down when the component starts; it must open its ports and keep running (P1.2).
func stepStartWithDepsDown(ctx context.Context, r *Run) {
	if r.pg != nil {
		_ = r.pg.Stop(ctx)
	}
	if r.nats != nil {
		_ = r.nats.C.Stop(ctx, 1)
	}
	r.authzSrv.Stop()
	if err := r.startMain(ctx); err != nil {
		r.ev.fail("CP-CORE-03", "starting the component: %v", err)
		return
	}
	if _, err := waitStatus(ctx, r.base+"/healthz", 200, r.startPeriod()); err != nil {
		running, code, _ := r.main.Running(ctx)
		r.ev.fail("CP-CORE-03", "/healthz not 200 within %v with dependencies down (running=%v, exit %d): %v", r.startPeriod(), running, code, err)
		r.mainUp = running
		return
	}
	time.Sleep(3 * time.Second)
	running, code, _ := r.main.Running(ctx)
	r.mainUp = running
	if !r.ev.check("CP-CORE-03", running, "the component exited (code %d) while PostgreSQL, the bus and authz were down (P1.2)", code) {
		return
	}
	x := r.call(ctx, "GET", "/readyz")
	waiting := strings.Split(x.metadata("waiting"), ",")
	ok := x.Status == 503 && x.reason() == "NOT_READY"
	r.ev.check("CP-CORE-05", ok, "/readyz before dependencies = %d %s, want 503 NOT_READY (P1.4)", x.Status, x.reason())
	if anyProtected(r.comp) {
		r.ev.check("CP-CORE-05", containsTrim(waiting, "bundle"), "metadata.waiting %q lacks bundle (P1.4)", x.metadata("waiting"))
	}
	if r.pg != nil {
		r.ev.check("CP-CORE-05", containsTrim(waiting, "db_identity"), "metadata.waiting %q lacks db_identity (P1.4)", x.metadata("waiting"))
	}
}

// CP-CORE-04 (before readiness): /healthz answers 200 while PostgreSQL is stopped.
func caseCore04(ctx context.Context, r *Run) {
	if !r.needMain("CP-CORE-04") {
		return
	}
	if r.pg == nil {
		r.ev.notApplicable("CP-CORE-04", "no database to stop; /healthz checked with the other dependencies down")
	}
	r.checkHealthz(ctx, "PostgreSQL, the bus and authz stopped, before readiness")
}

// CP-CORE-03 (verdict): after starting with every dependency down, the process is still the
// same one (never exited) and became ready once they came back.
func caseCore03(ctx context.Context, r *Run) {
	const id = "CP-CORE-03"
	if !r.needMain(id) {
		return
	}
	running, code, err := r.main.Running(ctx)
	r.ev.check(id, err == nil && running, "the component is not running any more (exit %d, %v)", code, err)
	r.ev.check(id, r.ready, "the component did not recover once its dependencies came back")
}

// checkHealthz: GET and HEAD /healthz answer 200 (P1.3).
func (r *Run) checkHealthz(ctx context.Context, when string) {
	for _, m := range []string{"GET", "HEAD"} {
		x := r.call(ctx, m, "/healthz")
		r.ev.check("CP-CORE-04", x.Err == nil && x.Status == 200, "%s /healthz with %s = %d %v, want 200 (P1.3)", m, when, x.Status, x.Err)
	}
}

func containsTrim(xs []string, s string) bool {
	for _, x := range xs {
		if strings.TrimSpace(x) == s {
			return true
		}
	}
	return false
}

// metadata returns problem metadata[k].
func (x *exchange) metadata(k string) string {
	m, _ := x.Problem["metadata"].(map[string]any)
	s, _ := m[k].(string)
	return s
}

// stepDatabaseBack is a setup step: PostgreSQL comes back while authz stays down.
func stepDatabaseBack(ctx context.Context, r *Run) {
	if r.pg == nil {
		return
	}
	if err := r.pg.Start(ctx); err != nil {
		r.ev.fail("CP-CORE-03", "restarting PostgreSQL: %v", err)
		return
	}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		x := r.call(ctx, "GET", "/readyz")
		w := strings.Split(x.metadata("waiting"), ",")
		if x.Status == 200 || (!containsTrim(w, "db_identity") && !containsTrim(w, "migrations")) {
			return
		}
		time.Sleep(time.Second)
	}
	r.ev.fail("CP-CORE-03", "45 s after PostgreSQL came back /readyz still waits for db_identity or migrations")
}

// CP-AUTH-10: before the first bundle the token is still verified first (no or a bad token
// answers 401 TOKEN_INVALID), then every route that is not Public answers 503
// AUTHZ_NOT_READY, Authenticated ones included; Public routes serve normally (P1.5, P6.2 as
// ruled for rc.2: fail closed, 401 before 503).
func caseAuth10(ctx context.Context, r *Run) {
	const id = "CP-AUTH-10"
	if !r.needMain(id) {
		return
	}
	n := 0
	for _, op := range r.comp.Operations {
		if !op.Protected() {
			continue
		}
		n++
		var body []reqOpt
		if op.Method != "GET" && op.Method != "DELETE" && op.Method != "HEAD" {
			body = append(body, withBody([]byte("{}")))
		}
		x := r.call(ctx, op.Method, r.target(op), body...)
		r.ev.check(id, x.Status == 401 && x.reason() == "TOKEN_INVALID",
			"%s %s without a token before the bundle = %d %s, want 401 TOKEN_INVALID (the token is verified before the bundle check)", op.Method, op.Path, x.Status, x.reason())
		x = r.call(ctx, op.Method, r.target(op), append(body, withToken(r.token(r.personaFor(op))))...)
		r.ev.check(id, x.Status == 503 && x.reason() == "AUTHZ_NOT_READY",
			"%s %s (guard %s) with a valid token before the bundle = %d %s, want 503 AUTHZ_NOT_READY (P1.5, P6.2)", op.Method, op.Path, op.Guard, x.Status, x.reason())
	}
	if n == 0 {
		r.ev.notApplicable(id, "no protected route")
	}
	if op, ok := r.publicOp(); ok {
		x := r.call(ctx, op.Method, r.target(op))
		r.ev.check(id, x.Err == nil && x.Status < 400, "Public %s %s before the bundle = %d, want it served (P1.5)", op.Method, op.Path, x.Status)
	} else {
		r.ev.pass(id, "no Public GET route to check")
	}
}

// stepProvidersBack is a setup step: authz and the bus come back; the component becomes ready.
func stepProvidersBack(ctx context.Context, r *Run) {
	if r.nats != nil {
		_ = r.nats.C.Start(ctx)
	}
	if err := r.authzSrv.Restart(); err != nil {
		r.ev.fail("CP-CORE-03", "restarting fake-authz: %v", err)
	}
	if !r.mainUp {
		return
	}
	_, err := waitStatus(ctx, r.base+"/readyz", 200, 45*time.Second)
	r.ev.check("CP-CORE-03", err == nil, "not ready within 45 s after every dependency came back: %v (P1.2 backoff ≤ 15 s)", err)
	r.ev.check("CP-CORE-05", err == nil, "/readyz did not become 200 once bundle, identity and migrations were in place: %v (P1.4)", err)
	r.ready = err == nil
}

// CP-CORE-05 (latch): once ready, stopping PostgreSQL or authz leaves /readyz at 200. Leaves
// authz stopped for CP-AUTH-13; stepAuthzBack restarts it.
func caseCore05Latch(ctx context.Context, r *Run) {
	const id = "CP-CORE-05"
	if !r.needMain(id) || !r.ready {
		r.ev.fail(id, "the component never became ready, the latch cannot be checked")
		r.authzSrv.Stop()
		return
	}
	stable := func(what string) {
		for i := 0; i < 3; i++ {
			x := r.call(ctx, "GET", "/readyz")
			if !r.ev.check(id, x.Status == 200, "/readyz with %s stopped after readiness = %d %s, want 200 (P1.4 latch)", what, x.Status, x.reason()) {
				return
			}
			time.Sleep(700 * time.Millisecond)
		}
	}
	if r.pg != nil {
		_ = r.pg.Stop(ctx)
		stable("PostgreSQL")
		r.checkHealthz(ctx, "PostgreSQL stopped, after readiness")
		if err := r.pg.Start(ctx); err != nil {
			r.ev.fail(id, "restarting PostgreSQL: %v", err)
		}
	}
	r.authzSrv.Stop()
	stable("authz")
}

// CP-AUTH-13: with authz stopped after the first bundle, decisions continue (fail-static).
func caseAuth13(ctx context.Context, r *Run) {
	const id = "CP-AUTH-13"
	if !r.needMain(id) {
		return
	}
	op, ok := r.probeOp()
	if !ok {
		r.ev.notApplicable(id, "no protected route")
		return
	}
	time.Sleep(2 * time.Second) // let a poll or two fail
	x := r.call(ctx, op.Method, r.target(op), withToken(r.token(r.personaFor(op))))
	r.ev.check(id, x.authorized(), "%s %s with authz stopped = %d %s, want the decision to continue (P6.1 fail-static)", op.Method, op.Path, x.Status, x.reason())
	if op.KeyGuarded() {
		x = r.call(ctx, op.Method, r.target(op), withToken(r.token(pNone)))
		r.ev.check(id, x.Status == 403 && x.reason() == "MISSING_PERMISSION",
			"%s %s without the key, authz stopped = %d %s, want 403 MISSING_PERMISSION", op.Method, op.Path, x.Status, x.reason())
	}
}

// stepAuthzBack is a setup step: authz runs again for the rest of the scenario.
func stepAuthzBack(_ context.Context, r *Run) {
	if !r.authzSrv.Running() {
		if err := r.authzSrv.Restart(); err != nil {
			r.ev.fail("CP-AUTH-13", "restarting fake-authz: %v", err)
		}
	}
}

// needMain records a failure when the main instance is not up.
func (r *Run) needMain(id string) bool {
	if r.mainUp {
		return true
	}
	r.ev.fail(id, "%s", fmt.Sprintf("the component is not running (see CP-CORE-03)"))
	return false
}
