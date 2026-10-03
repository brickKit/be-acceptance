package fakes

import (
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	resource "go.opentelemetry.io/proto/otlp/resource/v1"
	trace "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func otlpRequest(traceID, spanID, parent string) []byte {
	tid, _ := hex.DecodeString(traceID)
	sid, _ := hex.DecodeString(spanID)
	pid, _ := hex.DecodeString(parent)
	req := &coltrace.ExportTraceServiceRequest{ResourceSpans: []*trace.ResourceSpans{{
		Resource: &resource.Resource{Attributes: []*common.KeyValue{{Key: "service.name",
			Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: "conformance/rawstub"}}}}},
		ScopeSpans: []*trace.ScopeSpans{{Spans: []*trace.Span{{TraceId: tid, SpanId: sid, ParentSpanId: pid,
			Name: "GET /x", Kind: trace.Span_SPAN_KIND_SERVER}}}},
	}}}
	b, _ := proto.Marshal(req)
	return b
}

func TestObserverReceivesProtobufSpans(t *testing.T) {
	o := NewObserver()
	ts := httptest.NewServer(o.Handler())
	defer ts.Close()
	tid, sid, pid := strings.Repeat("ab", 16), strings.Repeat("01", 8), strings.Repeat("02", 8)
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	w.Write(otlpRequest(tid, sid, pid))
	w.Close()
	req, _ := http.NewRequest("POST", ts.URL+"/v1/traces", &gz)
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("post: %v %v", err, resp)
	}
	spans := o.SpansOfTrace(tid)
	if len(spans) != 1 || spans[0].ParentSpanID != pid || spans[0].Service != "conformance/rawstub" || spans[0].Kind != "server" {
		t.Fatalf("spans = %+v", spans)
	}
}

func TestObserverReceivesJSONSpans(t *testing.T) {
	o := NewObserver()
	ts := httptest.NewServer(o.Handler())
	defer ts.Close()
	body := `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"svc"}}]},
	 "scopeSpans":[{"spans":[{"traceId":"5B8EFFF798038103D269B633813FC60C","spanId":"EEE19B7EC3C1B174",
	 "parentSpanId":"EEE19B7EC3C1B173","name":"n","kind":2}]}]}]}`
	resp, err := http.Post(ts.URL+"/v1/traces", "application/json", strings.NewReader(body))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("post: %v", err)
	}
	spans := o.SpansOfTrace("5b8efff798038103d269b633813fc60c")
	if len(spans) != 1 || spans[0].ParentSpanID != "eee19b7ec3c1b173" || spans[0].Kind != "server" {
		t.Fatalf("spans = %+v", spans)
	}
	r2, _ := http.Post(ts.URL+"/v1/metrics", "application/json", strings.NewReader("{}"))
	if r2.StatusCode != 200 {
		t.Fatalf("metrics = %d", r2.StatusCode)
	}
}

func TestLogCaptureParsesJSONLines(t *testing.T) {
	l := NewLogs()
	l.Feed("stdout", `{"time":"2026-10-03T00:00:00Z","level":"info","msg":"http_request","request_id":"r1"}`)
	l.Feed("stdout", `not json`)
	l.Feed("stderr", `{"level":"error","msg":"x"}`)
	got := l.Find(func(e LogLine) bool { return e.JSON != nil && e.JSON["request_id"] == "r1" })
	if len(got) != 1 || got[0].Stream != "stdout" {
		t.Fatalf("find = %+v", got)
	}
	if n := len(l.Lines()); n != 3 {
		t.Fatalf("lines = %d", n)
	}
	if bad := l.Find(func(e LogLine) bool { return e.JSON == nil }); len(bad) != 1 {
		t.Fatalf("non-JSON lines = %d", len(bad))
	}
}
