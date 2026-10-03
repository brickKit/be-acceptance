package compconf

import (
	"context"
	"strconv"
	"time"
)

// CP-CORE-06: on SIGTERM in-flight requests finish, new ones are refused, and the process
// exits 0 within SHUTDOWN_GRACE, before stopGracePeriodSeconds (P1.6, P1.12). Ends the main
// instance.
func caseCore06(ctx context.Context, r *Run) {
	const id = "CP-CORE-06"
	if !r.needMain(id) {
		return
	}
	var slow chan *exchange
	if s := r.comp.Fixtures.Slow; s != nil && s.Path != "" {
		slow = make(chan *exchange, 1)
		persona := pAll
		if s.Key != "" {
			persona = r.personaWith(s.Key)
		}
		target := r.fixtureTarget(*s, r.slowRecord(ctx), map[string]string{"ms": strconv.Itoa(r.slowMS(*s, 3000))})
		tok := r.token(persona)
		go func() { slow <- r.call(ctx, s.Method, target, withToken(tok)) }()
		time.Sleep(700 * time.Millisecond)
	} else {
		r.ev.notApplicable(id, "fixtures have no slow operation: in-flight completion not observed")
	}
	sent := time.Now()
	if !r.ev.check(id, r.main.Signal(ctx, "TERM") == nil, "docker kill -s TERM failed") {
		return
	}
	time.Sleep(500 * time.Millisecond)
	if x := r.call(ctx, "GET", "/healthz"); x.Err == nil && x.Status < 300 {
		r.ev.fail(id, "a new request after SIGTERM was served (%d); new requests must be refused (P1.6)", x.Status)
	}
	if slow != nil {
		x := <-slow
		r.ev.check(id, x.Err == nil && x.Status == 200, "the in-flight slow request ended %d %v, want 200 (P1.6)", x.Status, x.Err)
	}
	limit := time.Duration(r.comp.Manifest.Deployment.StopGracePeriodSeconds) * time.Second
	code, err := r.main.Wait(ctx, limit+5*time.Second)
	took := time.Since(sent)
	r.mainUp = false
	r.ev.check(id, err == nil && code == 0, "exit code after SIGTERM = %d (%v), want 0 (P1.6)", code, err)
	r.ev.check(id, took < limit, "exited %v after SIGTERM, not before stopGracePeriodSeconds %v (P1.12)", took.Round(100*time.Millisecond), limit)
	r.ev.check(id, took <= r.shutdownGrace()+2*time.Second, "exited %v after SIGTERM, beyond SHUTDOWN_GRACE %v (P1.6)", took.Round(100*time.Millisecond), r.shutdownGrace())
}

// slowMS keeps a slow request below its route deadline (x-be-deadline-seconds, else
// HTTP_DEFAULT_TIMEOUT): at most want, and 1 s less than the deadline.
func (r *Run) slowMS(s FixtureOp, want int) int {
	deadline := 10 * time.Second
	if d, err := time.ParseDuration(r.envValue("HTTP_DEFAULT_TIMEOUT")); err == nil {
		deadline = d
	}
	for _, o := range r.comp.Operations {
		if o.Method == s.Method && matchTemplate(o.Path, s.Path) && o.DeadlineSeconds > 0 {
			deadline = time.Duration(o.DeadlineSeconds) * time.Second
		}
	}
	if ms := int(deadline.Milliseconds()) - 1000; ms < want {
		return max(ms, 100)
	}
	return want
}
