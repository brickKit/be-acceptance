package fakes

import (
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"

	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// Observer is the telemetry half of fake/observer: an OTLP/HTTP receiver (traces kept,
// metrics and logs accepted and dropped) at OTEL_BASE_URL.
type Observer struct {
	mu    sync.Mutex
	spans []Span
}

// Span is one received span; IDs are lowercase hex.
type Span struct {
	TraceID, SpanID, ParentSpanID string
	Name, Kind, Service           string
	Resource                      map[string]string
	Links                         []string // linked trace IDs
}

// NewObserver makes an empty receiver.
func NewObserver() *Observer { return &Observer{} }

// Handler serves /v1/traces, /v1/metrics, /v1/logs.
func (o *Observer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/traces", o.traces)
	discard := func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body) }
	mux.HandleFunc("POST /v1/metrics", discard)
	mux.HandleFunc("POST /v1/logs", discard)
	return mux
}

// Spans returns every span received.
func (o *Observer) Spans() []Span {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]Span(nil), o.spans...)
}

// SpansOfTrace returns the spans of one trace (hex trace ID, any case).
func (o *Observer) SpansOfTrace(traceID string) []Span {
	traceID = strings.ToLower(traceID)
	var out []Span
	for _, s := range o.Spans() {
		if s.TraceID == traceID {
			out = append(out, s)
		}
	}
	return out
}

func (o *Observer) traces(w http.ResponseWriter, r *http.Request) {
	var body io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body = zr
	}
	b, err := io.ReadAll(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var spans []Span
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		spans, err = decodeJSONSpans(b)
	} else {
		spans, err = decodeProtoSpans(b)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	o.mu.Lock()
	o.spans = append(o.spans, spans...)
	o.mu.Unlock()
	w.Header().Set("Content-Type", r.Header.Get("Content-Type"))
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		_, _ = w.Write([]byte("{}"))
	}
}

var spanKinds = map[int]string{0: "unspecified", 1: "internal", 2: "server", 3: "client", 4: "producer", 5: "consumer"}

func decodeProtoSpans(b []byte) ([]Span, error) {
	var req coltrace.ExportTraceServiceRequest
	if err := proto.Unmarshal(b, &req); err != nil {
		return nil, err
	}
	var out []Span
	for _, rs := range req.ResourceSpans {
		res := map[string]string{}
		for _, kv := range rs.GetResource().GetAttributes() {
			res[kv.Key] = kv.GetValue().GetStringValue()
		}
		for _, ss := range rs.ScopeSpans {
			for _, s := range ss.Spans {
				out = append(out, protoSpan(s, res))
			}
		}
	}
	return out, nil
}

func protoSpan(s *tracepb.Span, res map[string]string) Span {
	sp := Span{TraceID: hex.EncodeToString(s.TraceId), SpanID: hex.EncodeToString(s.SpanId),
		ParentSpanID: hex.EncodeToString(s.ParentSpanId), Name: s.Name, Kind: spanKinds[int(s.Kind)],
		Service: res["service.name"], Resource: res}
	for _, l := range s.Links {
		sp.Links = append(sp.Links, hex.EncodeToString(l.TraceId))
	}
	return sp
}

// OTLP JSON encodes trace and span IDs as hex strings, not base64 (OTLP spec), so protojson
// cannot read it; this decodes the few fields the suite needs.
type jsonExport struct {
	ResourceSpans []struct {
		Resource struct {
			Attributes []jsonKV `json:"attributes"`
		} `json:"resource"`
		ScopeSpans []struct {
			Spans []struct {
				TraceID      string `json:"traceId"`
				SpanID       string `json:"spanId"`
				ParentSpanID string `json:"parentSpanId"`
				Name         string `json:"name"`
				Kind         any    `json:"kind"`
				Links        []struct {
					TraceID string `json:"traceId"`
				} `json:"links"`
			} `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"resourceSpans"`
}

type jsonKV struct {
	Key   string `json:"key"`
	Value struct {
		StringValue string `json:"stringValue"`
	} `json:"value"`
}

func decodeJSONSpans(b []byte) ([]Span, error) {
	var req jsonExport
	if err := json.Unmarshal(b, &req); err != nil {
		return nil, err
	}
	var out []Span
	for _, rs := range req.ResourceSpans {
		res := map[string]string{}
		for _, kv := range rs.Resource.Attributes {
			res[kv.Key] = kv.Value.StringValue
		}
		for _, ss := range rs.ScopeSpans {
			for _, s := range ss.Spans {
				sp := Span{TraceID: strings.ToLower(s.TraceID), SpanID: strings.ToLower(s.SpanID),
					ParentSpanID: strings.ToLower(s.ParentSpanID), Name: s.Name, Kind: jsonKind(s.Kind),
					Service: res["service.name"], Resource: res}
				for _, l := range s.Links {
					sp.Links = append(sp.Links, strings.ToLower(l.TraceID))
				}
				out = append(out, sp)
			}
		}
	}
	return out, nil
}

func jsonKind(k any) string {
	switch v := k.(type) {
	case float64:
		return spanKinds[int(v)]
	case string: // SPAN_KIND_SERVER
		return strings.ToLower(strings.TrimPrefix(v, "SPAN_KIND_"))
	}
	return "unspecified"
}
