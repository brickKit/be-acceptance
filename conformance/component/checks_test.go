package compconf

import (
	"net/http"
	"strings"
	"testing"

	"github.com/brickKit/be-acceptance/conformance/component/fakes"
)

func problemExchange(status int, body string) *exchange {
	h := http.Header{}
	h.Set("Content-Type", "application/problem+json")
	h.Set("X-Request-Id", "rid-1")
	x := &exchange{Method: "GET", Path: "/conformance/rawstub/notes", Status: status, Header: h, Body: []byte(body)}
	x.parseProblem()
	return x
}

const goodProblem = `{"type":"urn:be:be:TOKEN_INVALID","title":"t","status":401,"code":"UNAUTHENTICATED",
 "reason":"TOKEN_INVALID","domain":"be","detail":"d","metadata":{},"instance":"/conformance/rawstub/notes",
 "request_id":"rid-1","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736"}`

func TestProblemIssues(t *testing.T) {
	cats, err := loadCatalogs(rawstub(t))
	if err != nil {
		t.Fatal(err)
	}
	if is := problemIssues(problemExchange(401, goodProblem), "conformance/rawstub", cats); len(is) != 0 {
		t.Fatalf("good problem refused: %v", is)
	}
	bad := map[string]string{
		"status mismatch":   strings.Replace(goodProblem, `"status":401`, `"status":400`, 1),
		"code mapping":      strings.Replace(goodProblem, `"code":"UNAUTHENTICATED"`, `"code":"PERMISSION_DENIED"`, 1),
		"uncatalogued":      strings.NewReplacer(`TOKEN_INVALID`, `NO_SUCH_REASON`).Replace(goodProblem),
		"request_id":        strings.Replace(goodProblem, `"request_id":"rid-1"`, `"request_id":"other"`, 1),
		"instance":          strings.Replace(goodProblem, `"instance":"/conformance/rawstub/notes"`, `"instance":"/x"`, 1),
		"schema (no trace)": strings.Replace(goodProblem, `,"trace_id":"4bf92f3577b34da6a3ce929d0e0e4736"`, ``, 1),
		"own domain":        strings.NewReplacer(`"domain":"be"`, `"domain":"conformance/rawstub"`, `urn:be:be:`, `urn:be:conformance/rawstub:`).Replace(goodProblem),
	}
	for name, body := range bad {
		if is := problemIssues(problemExchange(401, body), "conformance/rawstub", cats); len(is) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
	x := problemExchange(401, goodProblem)
	x.Header.Set("Content-Type", "application/json")
	if is := problemIssues(x, "conformance/rawstub", cats); len(is) == 0 {
		t.Error("wrong content type accepted")
	}
	tooLarge := `{"type":"urn:be:be:BODY_TOO_LARGE","title":"t","status":413,"code":"INVALID_ARGUMENT","reason":"BODY_TOO_LARGE",
	 "domain":"be","detail":"d","metadata":{},"instance":"/conformance/rawstub/notes","request_id":"rid-1","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736"}`
	if is := problemIssues(problemExchange(413, tooLarge), "conformance/rawstub", cats); len(is) != 0 {
		t.Fatalf("413 BODY_TOO_LARGE refused: %v", is)
	}
	own := `{"type":"urn:be:conformance/rawstub:NOTE_TITLE_REQUIRED","title":"t","status":400,"code":"INVALID_ARGUMENT",
	 "reason":"NOTE_TITLE_REQUIRED","domain":"conformance/rawstub","detail":"d","metadata":{},"instance":"/conformance/rawstub/notes",
	 "request_id":"rid-1","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736"}`
	if is := problemIssues(problemExchange(400, own), "conformance/rawstub", cats); len(is) != 0 {
		t.Fatalf("own reason refused: %v", is)
	}
}

func line(raw string) fakes.LogLine {
	l := fakes.NewLogs()
	l.Feed("stdout", raw)
	return l.Lines()[0]
}

func TestLogEnvelopeIssues(t *testing.T) {
	ok := `{"time":"2026-10-03T01:02:03.123456789Z","level":"info","msg":"x","component_id":"a/b","component_version":"1.0.0"}`
	if is := logEnvelopeIssues(line(ok), "a/b", "1.0.0"); len(is) != 0 {
		t.Fatalf("good line refused: %v", is)
	}
	for name, raw := range map[string]string{
		"not json":   "hello",
		"no level":   `{"time":"2026-10-03T01:02:03Z","msg":"x","component_id":"a/b","component_version":"1.0.0"}`,
		"bad level":  strings.Replace(ok, `"info"`, `"INFO"`, 1),
		"local time": strings.Replace(ok, `Z"`, `+08:00"`, 1),
		"other id":   strings.Replace(ok, `"a/b"`, `"c/d"`, 1),
		"too long":   strings.Replace(ok, `"x"`, `"`+strings.Repeat("y", 2100)+`"`, 1),
	} {
		if is := logEnvelopeIssues(line(raw), "a/b", "1.0.0"); len(is) == 0 {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestAccessLineIssues(t *testing.T) {
	ok := `{"time":"2026-10-03T01:02:03Z","level":"info","msg":"http_request","component_id":"a/b","component_version":"1",
	 "trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","span_id":"00f067aa0ba902b7","request_id":"r",
	 "http.request.method":"GET","http.route":"/a/b/notes/{id}","http.response.status_code":404,"duration_ms":1.5}`
	if is := accessLineIssues(line(ok)); len(is) != 0 {
		t.Fatalf("good access line refused: %v", is)
	}
	if is := accessLineIssues(line(strings.Replace(ok, `"span_id":"00f067aa0ba902b7",`, ``, 1))); len(is) == 0 {
		t.Error("missing span_id accepted")
	}
	if is := accessLineIssues(line(strings.Replace(ok, `404`, `"404"`, 1))); len(is) == 0 {
		t.Error("string status accepted")
	}
}

func TestLevelForCode(t *testing.T) {
	for code, lvl := range map[string]string{"INTERNAL": "error", "UNKNOWN": "error", "DATA_LOSS": "error",
		"UNAVAILABLE": "warn", "DEADLINE_EXCEEDED": "warn", "UNAUTHENTICATED": "info", "PERMISSION_DENIED": "info",
		"NOT_FOUND": "info", "INVALID_ARGUMENT": "info"} {
		if got := levelForCode(code); got != lvl {
			t.Errorf("%s -> %s, want %s", code, got, lvl)
		}
	}
}

const metricsText = `# HELP be_http_server_requests_total requests
# TYPE be_http_server_requests_total counter
be_http_server_requests_total{component="a/b",method="GET",route="/a/b/notes/{id}",status_code="404"} 3
# HELP be_http_server_duration_seconds d
# TYPE be_http_server_duration_seconds histogram
be_http_server_duration_seconds_bucket{component="a/b",method="GET",route="/a/b/notes/{id}",le="+Inf"} 3
be_http_server_duration_seconds_sum{component="a/b",method="GET",route="/a/b/notes/{id}"} 0.1
be_http_server_duration_seconds_count{component="a/b",method="GET",route="/a/b/notes/{id}"} 3
`

func TestMetricsIssues(t *testing.T) {
	if is := metricsIssues([]byte(metricsText), "a/b", "0192zz"); len(is) != 0 {
		t.Fatalf("good metrics refused: %v", is)
	}
	raw := strings.ReplaceAll(metricsText, "/a/b/notes/{id}", "/a/b/notes/0192zz")
	if is := metricsIssues([]byte(raw), "a/b", "0192zz"); len(is) == 0 {
		t.Error("raw path in route accepted")
	}
	nolabel := metricsText + "# TYPE go_goroutines gauge\ngo_goroutines 7\n"
	if is := metricsIssues([]byte(nolabel), "a/b", "x"); len(is) == 0 {
		t.Error("series without component label accepted")
	}
	if is := metricsIssues([]byte("# TYPE x counter\nx{component=\"a/b\"} 1\n"), "a/b", "x"); len(is) == 0 {
		t.Error("missing be_http_server_* accepted")
	}
}

func TestLeakIssues(t *testing.T) {
	p := map[string]any{"detail": "Something went wrong.", "title": "Internal error", "instance": "/a/notes"}
	if is := leakIssues(p, []string{"notes", "cc_1"}); len(is) != 0 {
		t.Fatalf("generic body refused: %v", is)
	}
	p["detail"] = `ERROR: permission denied for table notes (SQLSTATE 42501)`
	if is := leakIssues(p, []string{"notes"}); len(is) == 0 {
		t.Fatal("SQL error accepted")
	}
}
