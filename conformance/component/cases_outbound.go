package compconf

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/brickKit/be-acceptance/conformance/component/fakes"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/descriptorpb"
)

// The outbound profile (P7.2, P7.6–P7.9, P8.1, P8.2, P9.1): fixtures operations whose
// triggers name a dependency's rpc or user-plane route; the fake peer observes the calls.

type trigger struct {
	res, name string
	op        FixtureOp
	peer      *fakes.Peer
	method    string // full rpc name, or "<METHOD> <path>" for HTTP
	id        string // the record the operation acts on
}

// triggers lists the fixtures REST operations that call a dependency over gRPC (http=false)
// or over the user plane (http=true), each with a record prepared.
func (r *Run) triggers(ctx context.Context, http bool) []trigger {
	var out []trigger
	for _, res := range sortedKeys(r.comp.Fixtures.Resources) {
		for _, name := range sortedKeys(r.comp.Fixtures.Resources[res]) {
			op := r.comp.Fixtures.Resources[res][name]
			if op.Triggers == nil || op.Path == "" {
				continue
			}
			call := op.Triggers.GRPC
			if http {
				call = op.Triggers.HTTP
			}
			if call == "" {
				continue
			}
			t := trigger{res: res, name: name, op: op, method: call}
			for _, p := range r.peers {
				if _, ok := p.Method(call); ok || http {
					t.peer = p
				}
			}
			if http {
				t.method = r.httpRoute(call)
			}
			if t.peer == nil {
				continue
			}
			if strings.Contains(op.Path, "{id}") {
				id, _, err := r.create(ctx, res, "")
				if err != nil {
					continue
				}
				t.id = id
			}
			out = append(out, t)
		}
	}
	return out
}

// httpRoute turns "GET /conformance/peer/owners/{sub:alice}" into the fake peer's route key
// "GET /conformance/peer/owners/{owner_id}" it was configured with.
func (r *Run) httpRoute(call string) string {
	m, p, _ := strings.Cut(call, " ")
	for dep, calls := range r.comp.Fixtures.Dependencies {
		_ = dep
		for k := range calls {
			km, kp, ok := strings.Cut(k, " ")
			if ok && km == m && matchTemplate(kp, r.substitute(p, "")) {
				return k
			}
		}
	}
	return call
}

func (r *Run) needTriggers(ctx context.Context, id string, http bool) []trigger {
	if !r.needMain(id) {
		return nil
	}
	ts := r.triggers(ctx, http)
	if len(ts) == 0 {
		what := "rpc of a dependency"
		if http {
			what = "user-plane route of a dependency"
		}
		r.ev.notApplicable(id, "no fixtures operation triggers an "+what)
	}
	return ts
}

// fire runs the trigger operation once.
func (r *Run) fire(ctx context.Context, t trigger, opts ...reqOpt) *exchange {
	return r.invoke(ctx, t.op, t.id, "", map[string]string{"ms": "0"}, opts...)
}

// routeDeadline is the operation's x-be-deadline-seconds, else HTTP_DEFAULT_TIMEOUT.
func (r *Run) routeDeadline(op FixtureOp) time.Duration {
	d := 10 * time.Second
	if v, err := time.ParseDuration(r.envValue("HTTP_DEFAULT_TIMEOUT")); err == nil {
		d = v
	}
	for _, o := range r.comp.Operations {
		if o.Method == op.Method && matchTemplate(o.Path, op.Path) && o.DeadlineSeconds > 0 {
			d = time.Duration(o.DeadlineSeconds) * time.Second
		}
	}
	return d
}

// withPeerAnswer replaces a method's answer for the duration of f.
func withPeerAnswer(p *fakes.Peer, method string, a fakes.PeerAnswer, f func()) {
	prev := p.Answer(method)
	p.SetAnswer(method, a)
	defer func() { p.SetAnswer(method, prev); p.Release() }()
	f()
}

// CP-OUT-01: fifty triggers, ten at a time, reach the peer over one TCP connection (P7.6).
func caseOut01(ctx context.Context, r *Run) {
	const id = "CP-OUT-01"
	for _, t := range r.needTriggers(ctx, id, false) {
		r.fire(ctx, t) // the connection is dialled lazily
		before := t.peer.Connections()
		var wg sync.WaitGroup
		sem := make(chan struct{}, 10)
		for i := 0; i < 50; i++ {
			wg.Add(1)
			sem <- struct{}{}
			go func() { defer wg.Done(); r.fire(ctx, t); <-sem }()
		}
		wg.Wait()
		n := t.peer.Connections() - before
		r.ev.check(id, n == 0, "%s: 50 triggers opened %d new connections to %s, want the one already open (P7.6)", t.op.Path, n, t.peer.ID)
		return
	}
}

// CP-OUT-02: the outbound deadline is at most 3 s and below the inbound remainder (P7.7, P9.1).
func caseOut02(ctx context.Context, r *Run) {
	const id = "CP-OUT-02"
	for _, t := range r.needTriggers(ctx, id, false) {
		start := time.Now()
		x := r.fire(ctx, t)
		calls := callsSince(t.peer, t.method, start)
		if !r.ev.check(id, len(calls) > 0, "%s %s (%d) made no call to %s", t.op.Method, t.op.Path, x.Status, t.method) {
			continue
		}
		c := calls[0]
		limit := min(3*time.Second, r.routeDeadline(t.op))
		r.ev.check(id, c.Deadline > 0, "%s reached %s without grpc-timeout (P7.7)", t.op.Path, t.method)
		r.ev.check(id, c.Deadline <= limit+100*time.Millisecond, "%s gave %s a deadline of %v, want at most min(3 s, route deadline %v − 50 ms) (P7.7, P9.1)",
			t.op.Path, t.method, c.Deadline.Round(time.Millisecond), r.routeDeadline(t.op))
		r.ev.pass(id, fmt.Sprintf("%s: %v", t.method, c.Deadline.Round(10*time.Millisecond)))
	}
}

func callsSince(p *fakes.Peer, method string, t time.Time) []fakes.Call {
	var out []fakes.Call
	for _, c := range p.Calls(method) {
		if !c.At.Before(t) {
			out = append(out, c)
		}
	}
	return out
}

// retryable: the method's idempotency_level is NO_SIDE_EFFECTS or IDEMPOTENT (P7.8).
func retryable(p *fakes.Peer, method string) bool {
	md, ok := p.Method(method)
	if !ok {
		return false
	}
	o, _ := md.Options().(*descriptorpb.MethodOptions)
	l := o.GetIdempotencyLevel()
	return l == descriptorpb.MethodOptions_NO_SIDE_EFFECTS || l == descriptorpb.MethodOptions_IDEMPOTENT
}

// CP-OUT-03: on UNAVAILABLE an idempotent method is tried 3 times in all, another method once
// (P7.8).
func caseOut03(ctx context.Context, r *Run) {
	const id = "CP-OUT-03"
	for _, t := range r.needTriggers(ctx, id, false) {
		want := 1
		if retryable(t.peer, t.method) {
			want = 3
		}
		withPeerAnswer(t.peer, t.method, fakes.PeerAnswer{Code: codes.Unavailable}, func() {
			start := time.Now()
			x := r.fire(ctx, t)
			n := len(callsSince(t.peer, t.method, start))
			r.ev.check(id, n == want, "%s while %s answers UNAVAILABLE: %d attempts, want %d (P7.8) [answer %d %s]", t.op.Path, t.method, n, want, x.Status, x.reason())
		})
		time.Sleep(time.Second)
	}
}

// CP-OUT-04: while a dependency always answers UNAVAILABLE, retries stay within the
// retryThrottling budget (≤ 10 % in steady state, after the initial tokens), and each answer is
// 503 DEPENDENCY_UNAVAILABLE naming the dependency (P7.8, P4).
func caseOut04(ctx context.Context, r *Run) {
	const id = "CP-OUT-04"
	for _, t := range r.needTriggers(ctx, id, false) {
		if !retryable(t.peer, t.method) {
			continue
		}
		const n = 40
		withPeerAnswer(t.peer, t.method, fakes.PeerAnswer{Code: codes.Unavailable}, func() {
			start := time.Now()
			for i := 0; i < n; i++ {
				x := r.fire(ctx, t)
				if i == n-1 {
					r.ev.check(id, x.Status == 503 && x.reason() == "DEPENDENCY_UNAVAILABLE" && x.metadata("dependency") == t.peer.ID,
						"%s while %s is unavailable = %d %s dependency=%q, want 503 DEPENDENCY_UNAVAILABLE naming %s (P4)", t.op.Path, t.method, x.Status, x.reason(), x.metadata("dependency"), t.peer.ID)
				}
			}
			attempts := len(callsSince(t.peer, t.method, start))
			r.ev.check(id, attempts-n <= n/10+10, "%d calls for %d requests while always UNAVAILABLE: %d retries, over the retryThrottling budget (P7.8)", attempts, n, attempts-n)
			r.ev.pass(id, fmt.Sprintf("%d attempts for %d requests", attempts, n))
		})
		return
	}
}

// CP-OUT-05: with the peer hung, 70 concurrent triggers: at most 64 calls reach it and the
// others fail at once with 429 OUTBOUND_LIMIT (P7.9).
func caseOut05(ctx context.Context, r *Run) {
	const id = "CP-OUT-05"
	for _, t := range r.needTriggers(ctx, id, false) {
		withPeerAnswer(t.peer, t.method, fakes.PeerAnswer{Hang: true}, func() {
			t.peer.ResetMaxInflight()
			var mu sync.Mutex
			var wg sync.WaitGroup
			fast := 0
			seen := map[string]int{}
			for i := 0; i < 70; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					x := r.fire(ctx, t)
					mu.Lock()
					defer mu.Unlock()
					seen[fmt.Sprintf("%d %s", x.Status, x.reason())]++
					if x.Status == 429 && x.reason() == "OUTBOUND_LIMIT" && x.Took < time.Second {
						fast++
					}
				}()
			}
			wg.Wait()
			most := t.peer.MaxInflight()
			r.ev.check(id, most <= 64, "%d calls to %s were in progress at once, want at most 64 (P7.9)", most, t.peer.ID)
			r.ev.check(id, fast >= 1, "70 concurrent %s with %s hung: no request failed at once with 429 OUTBOUND_LIMIT (P7.9); answers %v, at most %d in progress", t.op.Path, t.method, seen, t.peer.MaxInflight())
			r.ev.pass(id, fmt.Sprintf("%d refused at once, at most %d calls in progress", fast, most))
		})
		return
	}
}

// CP-OUT-07: with the peer hung the component answers 504 within the route deadline (P3.4,
// P9.1).
func caseOut07(ctx context.Context, r *Run) {
	const id = "CP-OUT-07"
	for _, t := range r.needTriggers(ctx, id, false) {
		withPeerAnswer(t.peer, t.method, fakes.PeerAnswer{Hang: true}, func() {
			x := r.fire(ctx, t)
			d := r.routeDeadline(t.op)
			r.ev.check(id, x.Status == 504, "%s with %s hung = %d %s %v, want 504 (P3.4)", t.op.Path, t.method, x.Status, x.reason(), x.Err)
			r.ev.check(id, x.Took <= d+time.Second, "%s with %s hung answered after %v, route deadline %v (P9.1)", t.op.Path, t.method, x.Took.Round(100*time.Millisecond), d)
		})
	}
}

// CP-OUT-08: an outbound call carries be-caller = the component ID, be-actor-sub = the
// requesting user's sub, x-request-id and the request's trace (P7.2).
func caseOut08(ctx context.Context, r *Run) {
	const id = "CP-OUT-08"
	for _, t := range r.needTriggers(ctx, id, false) {
		persona := r.opPersona(t.op)
		tid := strings.ReplaceAll(uuidv7(), "-", "")
		rid := "compconf-out08-" + tid[:8]
		start := time.Now()
		r.invoke(ctx, t.op, t.id, persona, map[string]string{"ms": "0"},
			withHeader("traceparent", "00-"+tid+"-"+tid[:16]+"-01"), withHeader("X-Request-Id", rid))
		calls := callsSince(t.peer, t.method, start)
		if !r.ev.check(id, len(calls) > 0, "%s made no call to %s", t.op.Path, t.method) {
			continue
		}
		md := calls[0].Metadata
		get := func(k string) string {
			if v := md.Get(k); len(v) > 0 {
				return v[0]
			}
			return ""
		}
		r.ev.check(id, get("be-caller") == r.comp.ID(), "be-caller %q, want %q (P7.2)", get("be-caller"), r.comp.ID())
		r.ev.check(id, get("be-actor-sub") == r.personas[persona].Sub, "be-actor-sub %q, want the caller's sub %q (P7.2)", get("be-actor-sub"), r.personas[persona].Sub)
		r.ev.check(id, strings.Contains(get("traceparent"), tid), "traceparent %q is not of the request's trace %s (P7.2)", get("traceparent"), tid)
		r.ev.check(id, get("x-request-id") == rid, "x-request-id %q, want %q (P7.2)", get("x-request-id"), rid)
	}
}

// CP-OUT-06: user-plane HTTP to a dependency forwards Authorization unchanged, X-Request-Id,
// traceparent and X-Authz-Revision, and no be-* header (P8.1, P8.2).
func caseOut06(ctx context.Context, r *Run) {
	const id = "CP-OUT-06"
	for _, t := range r.needTriggers(ctx, id, true) {
		persona := r.opPersona(t.op)
		tok := r.token(persona)
		tid := strings.ReplaceAll(uuidv7(), "-", "")
		rid := "compconf-out06-" + tid[:8]
		start := time.Now()
		x := r.callAt(ctx, r.base, t.op.Method, r.fixtureTarget(t.op, t.id, nil), withToken(tok),
			withHeader("traceparent", "00-"+tid+"-"+tid[:16]+"-01"), withHeader("X-Request-Id", rid), withHeader("X-Authz-Revision", "1"))
		var call *fakes.HTTPCall
		for _, c := range t.peer.HTTPCalls() {
			if !c.At.Before(start) {
				c := c
				call = &c
			}
		}
		if !r.ev.check(id, call != nil, "%s (%d) made no user-plane call to %s (P8.1)", t.op.Path, x.Status, t.method) {
			continue
		}
		h := call.Header
		r.ev.check(id, h.Get("Authorization") == "Bearer "+tok, "Authorization not forwarded unchanged (P8.1)")
		r.ev.check(id, h.Get("X-Request-Id") == rid, "X-Request-Id %q, want %q (P8.1)", h.Get("X-Request-Id"), rid)
		r.ev.check(id, strings.Contains(h.Get("traceparent"), tid), "traceparent %q not of the request's trace (P8.1)", h.Get("traceparent"))
		r.ev.check(id, h.Get("X-Authz-Revision") == "1", "X-Authz-Revision not forwarded (P8.1)")
		for k := range h {
			r.ev.check(id, !strings.HasPrefix(strings.ToLower(k), "be-"), "user-plane call carries %s (P8.1: no system-plane header)", k)
		}
	}
}
