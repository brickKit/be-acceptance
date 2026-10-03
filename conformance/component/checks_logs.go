package compconf

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/brickKit/be-acceptance/conformance/component/fakes"
)

var (
	hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)
	hex16 = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

var logLevels = map[string]bool{"debug": true, "info": true, "warn": true, "error": true}

// logEnvelopeIssues checks the fields every line carries (P18.2, log fields).
func logEnvelopeIssues(l fakes.LogLine, id, version string) []string {
	var is []string
	add := func(f string, a ...any) { is = append(is, fmt.Sprintf(f, a...)) }
	if l.JSON == nil {
		return []string{fmt.Sprintf("line is not one JSON object: %.120q", l.Raw)}
	}
	if len(l.Raw) > 2048 {
		add("line of %d bytes exceeds 2 KiB", len(l.Raw))
	}
	ts := l.Str("time")
	if t, err := time.Parse(time.RFC3339Nano, ts); err != nil || !strings.HasSuffix(ts, "Z") || t.IsZero() {
		add("time %q is not RFC 3339 UTC", ts)
	}
	if !logLevels[l.Str("level")] {
		add("level %q not one of debug, info, warn, error", l.Str("level"))
	}
	if l.Str("msg") == "" {
		add("msg missing")
	}
	if l.Str("component_id") != id || l.Str("component_version") != version {
		add("component_id/component_version %q %q, want %q %q", l.Str("component_id"), l.Str("component_version"), id, version)
	}
	return is
}

// accessLineIssues checks the fields of an http_request line (P3.10, P18.2).
func accessLineIssues(l fakes.LogLine) []string {
	var is []string
	if !hex32.MatchString(l.Str("trace_id")) {
		is = append(is, "trace_id missing or not 32 hex")
	}
	if !hex16.MatchString(l.Str("span_id")) {
		is = append(is, "span_id missing or not 16 hex")
	}
	for _, k := range []string{"request_id", "http.request.method", "http.route"} {
		if l.Str(k) == "" {
			is = append(is, k+" missing")
		}
	}
	for _, k := range []string{"http.response.status_code", "duration_ms"} {
		if _, ok := l.JSON[k].(float64); !ok {
			is = append(is, k+" missing or not a number")
		}
	}
	return is
}

// levelForCode is the log level the runtime gives an error of this code (P4.6); "" = not logged.
func levelForCode(code string) string {
	switch code {
	case "INTERNAL", "UNKNOWN", "DATA_LOSS":
		return "error"
	case "UNAVAILABLE", "DEADLINE_EXCEEDED":
		return "warn"
	case "CANCELLED":
		return ""
	}
	return "info"
}

// codeOfStatus is the canonical code of an HTTP status, for access lines without error.code.
func codeOfStatus(status int) string {
	switch {
	case status == 500:
		return "INTERNAL"
	case status == 503:
		return "UNAVAILABLE"
	case status == 504:
		return "DEADLINE_EXCEEDED"
	case status >= 400:
		return "INVALID_ARGUMENT" // any caller error: INFO
	}
	return ""
}
