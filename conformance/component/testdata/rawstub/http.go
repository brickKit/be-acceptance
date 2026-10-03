package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// route is one operation of the main port. guard is "public", "authenticated" or a permission
// key (P6.2); deadline and maxBody are the OpenAPI x-be-deadline-seconds and
// x-be-max-body-bytes (P3.13), zero meaning the defaults of P3.4 and P3.6.
type route struct {
	method, pattern string
	segs            []string
	guard           string
	deadline        time.Duration
	maxBody         int64
	handle          func(*App, *reqCtx) error
}

const (
	userPrefix     = "/conformance/rawstub"
	defaultMaxBody = 1 << 20
)

func routes() []*route {
	rs := []*route{
		{method: "GET", pattern: "/healthz", guard: "public", handle: (*App).healthz},
		{method: "GET", pattern: "/readyz", guard: "public", handle: (*App).readyz},
		{method: "GET", pattern: "/metrics", guard: "public", handle: (*App).metricsPage},
		{method: "GET", pattern: "/_be/info", guard: "public", handle: (*App).info},
		{method: "GET", pattern: userPrefix + "/kinds", guard: "public", handle: (*App).listKinds},
		{method: "PUT", pattern: userPrefix + "/owners/me", guard: "authenticated", handle: (*App).putOwner},
		{method: "GET", pattern: userPrefix + "/notes", guard: "conformance.rawstub.view", handle: (*App).listNotes},
		{method: "POST", pattern: userPrefix + "/notes", guard: "conformance.rawstub.create", handle: (*App).createNote},
		{method: "GET", pattern: userPrefix + "/notes/{id}", guard: "conformance.rawstub.view", handle: (*App).getNote},
		{method: "POST", pattern: userPrefix + "/notes/{id}/archive", guard: "conformance.rawstub.archive", handle: (*App).archiveNote},
		{method: "POST", pattern: userPrefix + "/slow", guard: "conformance.rawstub.view", deadline: 5 * time.Second, handle: (*App).slow},
	}
	rs = append(rs, lifecycleRoutes()...)
	for _, r := range rs {
		r.segs = strings.Split(strings.TrimPrefix(r.pattern, "/"), "/")
	}
	return rs
}

// match finds the route for method and path; HEAD matches GET routes.
func (a *App) match(method, path string) (*route, map[string]string) {
	if method == http.MethodHead {
		method = http.MethodGet
	}
	segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for _, r := range a.routes {
		if r.method != method || len(r.segs) != len(segs) {
			continue
		}
		params := map[string]string{}
		ok := true
		for i, s := range r.segs {
			if strings.HasPrefix(s, "{") {
				name, suffix, _ := strings.Cut(strings.TrimPrefix(s, "{"), "}") // {unit}:thaw
				if segs[i] == "" || !strings.HasSuffix(segs[i], suffix) || len(segs[i]) == len(suffix) {
					ok = false
					break
				}
				params[name] = strings.TrimSuffix(segs[i], suffix)
			} else if s != segs[i] {
				ok = false
				break
			}
		}
		if ok {
			return r, params
		}
	}
	return nil, nil
}

// reqCtx is one request's state.
type reqCtx struct {
	w      *recorder
	r      *http.Request
	ctx    context.Context
	route  *route
	params map[string]string
	span   *Span
	reqID  string
	claims *Claims
	perm   string
	err    *apiError
}

// logFields are the request fields every log line inside the request carries.
func (rc *reqCtx) logFields(f F) F {
	f["trace_id"], f["span_id"], f["request_id"] = rc.span.TraceID, rc.span.SpanID, rc.reqID
	if rc.claims != nil {
		f["sub"] = rc.claims.Sub
	}
	return f
}

type recorder struct {
	http.ResponseWriter
	status int
}

func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

// ServeHTTP runs every request through the same steps: span and request ID, deadline, route,
// body limit, guard, handler, then problem body, metrics, span export and the access log.
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rt, params := a.match(r.Method, r.URL.Path)
	tmpl := "unmatched"
	if rt != nil {
		tmpl = rt.pattern
	}
	span := startSpan(r.Method+" "+tmpl, r.Header.Get("traceparent"), r.Header.Get("tracestate"))
	reqID := r.Header.Get("X-Request-Id")
	if reqID == "" {
		reqID = span.TraceID
	}
	rec := &recorder{ResponseWriter: w}
	rec.Header().Set("X-Request-Id", reqID)
	deadline := a.cfg.HTTPDefaultTimeout
	if rt != nil && rt.deadline > 0 {
		deadline = rt.deadline
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(deadline + 5*time.Second))
	ctx, cancel := context.WithTimeout(r.Context(), deadline)
	defer cancel()
	rc := &reqCtx{w: rec, r: r, ctx: ctx, route: rt, params: params, span: span, reqID: reqID}
	if err := a.serve(rc); err != nil {
		a.writeError(rc, a.deadlineAware(rc, err))
	}
	a.finish(rc, tmpl, start)
}

func (a *App) serve(rc *reqCtx) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = internalErr(errors.New("panic: " + strings.TrimSpace(toString(p))))
		}
	}()
	if rc.route == nil {
		return beErr("NOT_FOUND", nil)
	}
	limit := int64(defaultMaxBody)
	if rc.route.maxBody > 0 {
		limit = rc.route.maxBody
	}
	if rc.r.ContentLength > limit {
		return beErr("BODY_TOO_LARGE", map[string]string{"limit": strconv.FormatInt(limit, 10)})
	}
	rc.r.Body = http.MaxBytesReader(rc.w, rc.r.Body, limit)
	if err := a.guard(rc); err != nil {
		return err
	}
	return rc.route.handle(a, rc)
}

func toString(p any) string {
	if e, ok := p.(error); ok {
		return e.Error()
	}
	if s, ok := p.(string); ok {
		return s
	}
	return "unknown panic"
}

// deadlineAware maps an error that ended because the route deadline passed (P3.4).
func (a *App) deadlineAware(rc *reqCtx, err error) error {
	var ae *apiError
	if errors.As(err, &ae) && ae.Code != "INTERNAL" {
		return err
	}
	if errors.Is(rc.r.Context().Err(), context.Canceled) {
		return beErrCause("REQUEST_CANCELLED", err)
	}
	if errors.Is(rc.ctx.Err(), context.DeadlineExceeded) {
		var de *dbError
		if errors.As(err, &de) {
			return beErrCause("STATEMENT_TIMEOUT", err)
		}
		return beErrCause("DEADLINE_BUDGET_EXHAUSTED", err)
	}
	return err
}

func (a *App) writeError(rc *reqCtx, err error) {
	ae := asAPIError(err)
	rc.err = ae
	for k, v := range ae.header {
		rc.w.Header().Set(k, v)
	}
	body := problemBody(ae, a.cfg.DefaultLocale, rc.r.URL.Path, rc.reqID, rc.span.TraceID, a.broken == "leak-internal")
	rc.w.Header().Set("Content-Type", "application/problem+json")
	rc.w.WriteHeader(body["status"].(int))
	_ = json.NewEncoder(rc.w).Encode(body)
}

func writeJSON(rc *reqCtx, status int, v any) error {
	rc.w.Header().Set("Content-Type", "application/json")
	rc.w.WriteHeader(status)
	return json.NewEncoder(rc.w).Encode(v)
}

// finish records metrics, exports the span and writes the access-log line (P3.10, P18.2).
func (a *App) finish(rc *reqCtx, tmpl string, start time.Time) {
	status := rc.w.status
	if status == 0 {
		status = http.StatusOK
	}
	dur := time.Since(start)
	method := rc.r.Method
	a.metrics.Inc("be_http_server_requests_total", method, tmpl, strconv.Itoa(status))
	a.metrics.Observe("be_http_server_duration_seconds", dur.Seconds(), method, tmpl)
	rc.span.Attrs["http.request.method"] = method
	rc.span.Attrs["http.route"] = tmpl
	rc.span.Attrs["http.response.status_code"] = status
	rc.span.Attrs["url.path"] = rc.r.URL.Path
	rc.span.Error = status >= 500
	a.exporter.Finish(rc.span)

	f := rc.logFields(F{
		"http.request.method": method, "http.route": tmpl,
		"http.response.status_code": status, "duration_ms": dur.Milliseconds(),
	})
	if rc.perm != "" {
		f["perm"] = rc.perm
	}
	level := "info"
	if rc.err != nil {
		n := rc.err.normalized()
		f["error.code"], f["error.reason"] = n.Code, n.Reason
		f["error"] = rootCause(rc.err).Error()
		level = accessLogLevel(n.Code)
	}
	a.log.Log(level, "http_request", f)
}

// errorLogWriter routes net/http's own messages into the JSON log.
type errorLogWriter struct{ log *Logger }

func (w errorLogWriter) Write(p []byte) (int, error) {
	w.log.Debug("http_server", F{"error": strings.TrimSpace(string(p))})
	return len(p), nil
}

func newHTTPServer(a *App) *http.Server {
	return &http.Server{
		Handler:           a,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      a.cfg.HTTPDefaultTimeout + 5*time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          log.New(errorLogWriter{a.log}, "", 0),
	}
}
