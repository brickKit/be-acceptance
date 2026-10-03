package compconf

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/brickKit/be-acceptance/conformance/component/fakes"
)

// CP-OBS-01: an inbound traceparent becomes the parent of the server span the OTLP sink sees,
// with the component's resource attributes (P3.3, P18.1).
func caseObs01(ctx context.Context, r *Run) {
	const id = "CP-OBS-01"
	if !r.needMain(id) {
		return
	}
	if !r.ev.check(id, r.comp.HasConfigKey("OTEL_BASE_URL"), "configSchema does not declare OTEL_BASE_URL, so spans cannot be exported (P18.1)") {
		return
	}
	op, ok := r.probeOp()
	opts := []reqOpt{}
	target := "/_be/info"
	if ok {
		target, opts = r.target(op), append(opts, withToken(r.token(r.personaFor(op))))
	}
	tid := strings.ReplaceAll(uuidv7(), "-", "")
	parent := tid[:16]
	r.call(ctx, "GET", target, append(opts, withHeader("traceparent", "00-"+tid+"-"+parent+"-01"))...)
	var spans []fakes.Span
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		if spans = r.obs.SpansOfTrace(tid); len(spans) > 0 {
			break
		}
	}
	if !r.ev.check(id, len(spans) > 0, "no span of trace %s reached the OTLP sink within 20 s (P18.1)", tid) {
		return
	}
	var server *fakes.Span
	for i := range spans {
		if spans[i].ParentSpanID == parent && spans[i].Kind == "server" {
			server = &spans[i]
		}
	}
	if !r.ev.check(id, server != nil, "no server span with parent %s among %d spans of the trace (P3.3)", parent, len(spans)) {
		return
	}
	r.ev.check(id, server.Service == r.comp.ID(), "service.name %q, want %q (P18.1)", server.Service, r.comp.ID())
	r.ev.check(id, server.Resource["service.version"] == r.comp.Version(), "service.version %q, want %q (P18.1)", server.Resource["service.version"], r.comp.Version())
}

// CP-OBS-03: /metrics names, the component label on every series, route templates (P18.3).
func caseObs03(ctx context.Context, r *Run) {
	const id = "CP-OBS-03"
	if !r.needMain(id) {
		return
	}
	raw := ""
	if op, ok := r.templatedOp(); ok {
		raw = infraToken()
		path := strings.Replace(op.Path, op.Path[strings.Index(op.Path, "{"):strings.Index(op.Path, "}")+1], raw, 1)
		r.call(ctx, op.Method, r.substitute(path, ""), withToken(r.token(r.personaFor(op))))
	}
	x := r.call(ctx, "GET", "/metrics")
	if !r.ev.check(id, x.Status == 200, "/metrics = %d", x.Status) {
		return
	}
	for _, is := range metricsIssues(x.Body, r.comp.ID(), raw) {
		r.ev.fail(id, "%s", is)
	}
	r.ev.pass(id)
}

func infraToken() string { return "cc" + strings.ReplaceAll(uuidv7(), "-", "")[:20] }

// CP-OBS-04 (request): the operation that logs personal data logs it [REDACTED] (P18.2).
func caseObs04PII(ctx context.Context, r *Run) {
	const id = "CP-OBS-04"
	if !r.needMain(id) {
		return
	}
	pii := r.comp.Fixtures.PIILog
	if pii == nil {
		r.ev.notApplicable(id, "fixtures have no pii_log; only secret and token leaks are checked")
		return
	}
	op, ok := r.fixtureOp(pii.Via)
	if !r.ev.check(id, ok, "pii_log.via %s is not a fixtures operation", pii.Via) {
		return
	}
	persona := op.As
	if persona == "" {
		persona = pAll
	}
	body := r.fixtureBody(op)
	start := time.Now()
	x := r.call(ctx, op.Method, r.fixtureTarget(op, "", nil), withToken(r.token(persona)), withBody(body))
	if !r.ev.check(id, x.Status < 400, "%s %s = %d %s", op.Method, op.Path, x.Status, x.reason()) {
		return
	}
	var lines []fakes.LogLine
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline) && len(lines) == 0; time.Sleep(200 * time.Millisecond) {
		lines = r.logs.Find(func(l fakes.LogLine) bool { return l.Str("msg") == pii.Msg && !l.At.Before(start) })
	}
	if !r.ev.check(id, len(lines) > 0, "no log line msg=%s after %s %s", pii.Msg, op.Method, op.Path) {
		return
	}
	var sent map[string]any
	_ = json.Unmarshal(body, &sent)
	for _, f := range pii.Fields {
		r.ev.check(id, lines[0].JSON[f] == "[REDACTED]", "field %s logged as %v, want \"[REDACTED]\" (P18.2)", f, lines[0].JSON[f])
		if v, ok := sent[f].(string); ok && len(v) > 3 {
			for _, l := range r.logs.Lines() {
				if strings.Contains(l.Raw, v) {
					r.ev.fail(id, "the value of %s appears in a log line", f)
					break
				}
			}
		}
	}
}

// CP-OBS-04 (end): no secret value and no access token in any line (P2.7, P18.2, P18.4).
func caseObs04Leaks(_ context.Context, r *Run) {
	const id = "CP-OBS-04"
	for _, l := range r.logs.Lines() {
		for k, v := range r.secrets {
			if len(v) >= 4 && strings.Contains(l.Raw, v) {
				r.ev.fail(id, "the value of secret %s appears in a log line", k)
			}
		}
		for _, t := range r.tokens {
			if sig := t[strings.LastIndex(t, ".")+1:]; len(sig) > 10 && strings.Contains(l.Raw, sig) {
				r.ev.fail(id, "an access token appears in a log line (P18.4)")
				break
			}
		}
	}
	r.ev.pass(id)
}

// CP-OBS-02: every line has the envelope; each access line has its fields, sub and perm on a
// protected route; error levels follow the code (P3.10, P4.6, P18.2, P18.4).
func caseObs02(_ context.Context, r *Run) {
	const id = "CP-OBS-02"
	lines := r.logs.Lines()
	n := 0
	for _, l := range lines {
		if l.Stream == "stderr" && strings.TrimSpace(l.Raw) != "" {
			r.failCapped(id, &n, "a line on stderr: logs go to stdout only (P18.2): %.120q", l.Raw)
			continue
		}
		for _, is := range logEnvelopeIssues(l, r.comp.ID(), r.comp.Version()) {
			r.failCapped(id, &n, "%s", is)
		}
		if code := l.Str("error.code"); code != "" && levelForCode(code) != l.Str("level") && levelForCode(code) != "" {
			r.failCapped(id, &n, "a line with error.code %s has level %s, want %s (P4.6)", code, l.Str("level"), levelForCode(code))
		}
	}
	r.checkAccessLines(id, lines, &n)
	r.ev.pass(id)
}

func (r *Run) failCapped(id string, n *int, f string, a ...any) {
	*n++
	if *n <= 12 {
		r.ev.fail(id, f, a...)
	}
}

// checkAccessLines finds the access line of every user-plane request the suite made.
func (r *Run) checkAccessLines(id string, lines []fakes.LogLine, n *int) {
	byRID := map[string]fakes.LogLine{}
	for _, l := range lines {
		if l.Str("msg") == "http_request" {
			byRID[l.Str("request_id")] = l
		}
	}
	prefix := "/" + r.comp.ID() + "/"
	found := 0
	for _, x := range r.rec.list() {
		rid := x.ReqHeader.Get("X-Request-Id")
		if x.Err != nil || rid == "" || !strings.HasPrefix(x.Path, prefix) {
			continue
		}
		l, ok := byRID[rid]
		if !ok {
			r.failCapped(id, n, "no http_request line for %s %s (request_id %s) (P3.10)", x.Method, x.Path, rid)
			continue
		}
		found++
		for _, is := range accessLineIssues(l) {
			r.failCapped(id, n, "access line of %s %s: %s", x.Method, x.Path, is)
		}
		if want := levelForCode(codeOfStatus(x.Status)); x.Status >= 400 && want != "" && l.Str("level") != want {
			r.failCapped(id, n, "access line of a %d answer has level %s, want %s (P4.6)", x.Status, l.Str("level"), want)
		}
		r.checkSubPerm(id, x, l, n)
	}
	r.ev.check(id, found > 0, "no access line found for any request")
}

func (r *Run) checkSubPerm(id string, x *exchange, l fakes.LogLine, n *int) {
	auth := x.ReqHeader.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || !x.authorized() || x.Status >= 500 {
		return
	}
	for _, op := range r.comp.Operations {
		if op.KeyGuarded() && op.Method == x.Method && matchTemplate(op.Path, x.Path) {
			if l.Str("perm") != op.Guard || l.Str("sub") == "" {
				r.failCapped(id, n, "access line of %s %s has sub %q perm %q, want the caller's sub and %s (P18.4)", x.Method, x.Path, l.Str("sub"), l.Str("perm"), op.Guard)
			}
			return
		}
	}
}

func matchTemplate(tmpl, path string) bool {
	ts, ps := strings.Split(tmpl, "/"), strings.Split(path, "/")
	if len(ts) != len(ps) {
		return false
	}
	for i := range ts {
		if !strings.HasPrefix(ts[i], "{") && ts[i] != ps[i] {
			return false
		}
	}
	return true
}

// CP-OBS-05: with LOG_LEVEL=warn a fresh instance writes no info or debug line (P18.2).
func caseObs05(ctx context.Context, r *Run) {
	const id = "CP-OBS-05"
	if !r.ev.check(id, r.comp.HasConfigKey("LOG_LEVEL"), "configSchema does not declare LOG_LEVEL (P2)") {
		return
	}
	logs := fakes.NewLogs()
	in, err := r.startInstance(ctx, "warn", envWith(r.compEnv, map[string]string{"LOG_LEVEL": "warn"}), logs)
	if in != nil {
		defer func() { _ = in.c.Remove(context.Background()); in.stop() }()
	}
	if !r.ev.check(id, err == nil, "starting an instance with LOG_LEVEL=warn: %v", err) {
		return
	}
	_, err = waitStatus(ctx, in.base+"/readyz", 200, 60*time.Second)
	r.ev.check(id, err == nil, "the LOG_LEVEL=warn instance did not become ready: %v", err)
	if op, ok := r.probeOp(); ok {
		send(ctx, nil, in.base, op.Method, r.target(op), withToken(r.token(r.personaFor(op))))
		send(ctx, nil, in.base, op.Method, r.target(op))
	}
	if op, ok := r.publicOp(); ok {
		send(ctx, nil, in.base, op.Method, r.target(op))
	}
	_ = in.c.Signal(ctx, "TERM")
	_, _ = in.c.Wait(ctx, 40*time.Second)
	time.Sleep(300 * time.Millisecond)
	for _, l := range logs.Lines() {
		lvl := l.Str("level")
		r.ev.check(id, lvl != "info" && lvl != "debug", "LOG_LEVEL=warn wrote a %s line: %.160s", lvl, l.Raw)
	}
	r.ev.pass(id, fmt.Sprintf("%d lines checked", len(logs.Lines())))
}
