package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// Span is one server span. Every request has one, also when nothing is exported (P18.1).
type Span struct {
	TraceID, SpanID, ParentID, TraceState string
	Name                                  string
	Kind                                  int // 2 = server
	Start, End                            time.Time
	Attrs                                 map[string]any
	Error                                 bool
	sampled                               bool
}

var traceparentRe = regexp.MustCompile(`^([0-9a-f]{2})-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})(-.*)?$`)

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// startSpan extracts W3C trace context (P3.3) and starts a server span: a valid parent makes
// the span its child, otherwise a new trace begins.
func startSpan(name, traceparent, tracestate string) *Span {
	s := &Span{Name: name, Kind: 2, Start: time.Now(), Attrs: map[string]any{}, sampled: true}
	if m := traceparentRe.FindStringSubmatch(traceparent); m != nil && m[1] != "ff" &&
		m[2] != "00000000000000000000000000000000" && m[3] != "0000000000000000" && (m[1] != "00" || m[5] == "") {
		s.TraceID, s.ParentID, s.TraceState = m[2], m[3], tracestate
		flags, _ := strconv.ParseUint(m[4], 16, 8)
		s.sampled = flags&1 == 1
	} else {
		s.TraceID = randHex(16)
	}
	s.SpanID = randHex(8)
	return s
}

// Exporter sends finished spans as OTLP/HTTP JSON to {OTEL_BASE_URL}/v1/traces; an empty base
// means no export and no error.
type Exporter struct {
	url      string
	resource []map[string]any
	ch       chan *Span
	client   *http.Client
	log      *Logger
	done     chan struct{}
	once     sync.Once
}

func newExporter(cfg *Config, log *Logger) *Exporter {
	host, _ := os.Hostname()
	e := &Exporter{ch: make(chan *Span, 4096), client: &http.Client{Timeout: 3 * time.Second}, log: log, done: make(chan struct{})}
	if cfg.OtelBaseURL != "" {
		e.url = cfg.OtelBaseURL + "/v1/traces"
	}
	e.resource = []map[string]any{
		attr("service.name", cfg.ComponentID), attr("service.version", cfg.ComponentVersion),
		attr("service.instance.id", host), attr("telemetry.sdk.language", "go"),
	}
	return e
}

func attr(k string, v any) map[string]any {
	switch t := v.(type) {
	case int:
		return map[string]any{"key": k, "value": map[string]any{"intValue": strconv.Itoa(t)}}
	case bool:
		return map[string]any{"key": k, "value": map[string]any{"boolValue": t}}
	}
	return map[string]any{"key": k, "value": map[string]any{"stringValue": v}}
}

// Finish ends the span and queues it for export.
func (e *Exporter) Finish(s *Span) {
	s.End = time.Now()
	if e.url == "" || !s.sampled {
		return
	}
	select {
	case e.ch <- s:
	default: // queue full: drop rather than block a request
	}
}

// Run batches spans until ctx ends, then flushes what is left.
func (e *Exporter) Run(ctx context.Context) {
	defer close(e.done)
	if e.url == "" {
		<-ctx.Done()
		return
	}
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	var batch []*Span
	for {
		select {
		case s := <-e.ch:
			batch = append(batch, s)
			if len(batch) >= 128 {
				e.send(batch)
				batch = nil
			}
		case <-tick.C:
			if len(batch) > 0 {
				e.send(batch)
				batch = nil
			}
		case <-ctx.Done():
			for {
				select {
				case s := <-e.ch:
					batch = append(batch, s)
				default:
					if len(batch) > 0 {
						e.send(batch)
					}
					return
				}
			}
		}
	}
}

// Wait blocks until Run has flushed, at most d.
func (e *Exporter) Wait(d time.Duration) {
	select {
	case <-e.done:
	case <-time.After(d):
	}
}

func (e *Exporter) send(batch []*Span) {
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("otlp_export_panic", F{"error": "panic in exporter"})
		}
	}()
	spans := make([]map[string]any, 0, len(batch))
	for _, s := range batch {
		spans = append(spans, spanJSON(s))
	}
	body, _ := json.Marshal(map[string]any{"resourceSpans": []any{map[string]any{
		"resource":   map[string]any{"attributes": e.resource},
		"scopeSpans": []any{map[string]any{"scope": map[string]any{"name": "rawstub"}, "spans": spans}},
	}}})
	resp, err := e.client.Post(e.url, "application/json", bytes.NewReader(body))
	if err != nil {
		e.log.Debug("otlp_export_failed", F{"error": err.Error()})
		return
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		e.log.Debug("otlp_export_failed", F{"error": "status " + strconv.Itoa(resp.StatusCode)})
	}
}

// spanJSON is the OTLP JSON encoding: ids as lowercase hex, times as decimal strings.
func spanJSON(s *Span) map[string]any {
	attrs := make([]map[string]any, 0, len(s.Attrs))
	for k, v := range s.Attrs {
		attrs = append(attrs, attr(k, v))
	}
	status := map[string]any{"code": 1}
	if s.Error {
		status["code"] = 2
	}
	m := map[string]any{
		"traceId": s.TraceID, "spanId": s.SpanID, "name": s.Name, "kind": s.Kind,
		"startTimeUnixNano": strconv.FormatInt(s.Start.UnixNano(), 10),
		"endTimeUnixNano":   strconv.FormatInt(s.End.UnixNano(), 10),
		"attributes":        attrs, "status": status,
	}
	if s.ParentID != "" {
		m["parentSpanId"] = s.ParentID
	}
	if s.TraceState != "" {
		m["traceState"] = s.TraceState
	}
	return m
}
