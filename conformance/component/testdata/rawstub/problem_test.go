package main

import (
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestProblemVectors runs the problem operation of vectors errors/problem.json: every member
// except title and detail, and detail never containing the original error.
func TestProblemVectors(t *testing.T) {
	for _, c := range loadVectors(t, "errors-problem.json") {
		if c.Op != "problem" {
			continue
		}
		t.Run(c.ID, func(t *testing.T) {
			var in struct {
				Error struct {
					Code, Reason, Domain string
					Metadata             map[string]any
					Violations           []violation
					InternalMessage      string `json:"internal_message"`
				}
				Request struct {
					Path      string `json:"path"`
					RequestID string `json:"request_id"`
					TraceID   string `json:"trace_id"`
				}
			}
			_ = json.Unmarshal(c.Input, &in)
			meta := map[string]string{}
			for k, v := range in.Error.Metadata {
				s, ok := v.(string)
				if !ok {
					checkVector(t, c, nil, "METADATA_NOT_STRING")
					return
				}
				meta[k] = s
			}
			e := &apiError{Code: in.Error.Code, Reason: in.Error.Reason, Domain: in.Error.Domain,
				Metadata: meta, Violations: in.Error.Violations}
			if in.Error.InternalMessage != "" {
				e.cause = errors.New(in.Error.InternalMessage)
			}
			body := problemBody(e, "en", in.Request.Path, in.Request.RequestID, in.Request.TraceID, false)
			var want struct {
				ContentType string          `json:"content_type"`
				Body        json.RawMessage `json:"body"`
				MustNot     []string        `json:"detail_must_not_contain"`
			}
			_ = json.Unmarshal(c.Expected, &want)
			detail := body["detail"].(string)
			if body["title"] == "" || detail == "" {
				t.Errorf("title and detail must be non-empty")
			}
			for _, s := range want.MustNot {
				if strings.Contains(detail, s) {
					t.Errorf("detail leaks %q", s)
				}
			}
			delete(body, "title")
			delete(body, "detail")
			if !jsonEqual(t, body, want.Body) {
				got, _ := json.Marshal(body)
				t.Errorf("got  %s\nwant %s", got, want.Body)
			}
		})
	}
}

// TestProblemShape checks the required members and the leak-internal broken variant.
func TestProblemShape(t *testing.T) {
	e := internalErr(errors.New("ERROR: permission denied for table notes (SQLSTATE 42501)"))
	body := problemBody(e, "zh-CN", "/conformance/rawstub/notes", "r-1", strings.Repeat("a", 32), false)
	for _, k := range []string{"type", "title", "status", "code", "reason", "domain", "detail", "metadata", "instance", "request_id", "trace_id"} {
		if _, ok := body[k]; !ok {
			t.Errorf("missing %s", k)
		}
	}
	if body["detail"] != "系统出错了。反馈时请提供 trace ID。" || body["title"] != "内部错误" {
		t.Errorf("zh texts: %v / %v", body["title"], body["detail"])
	}
	if _, bad := body["error"]; bad {
		t.Error("problem carries an error member")
	}
	leaky := problemBody(e, "en", "/x", "r", strings.Repeat("a", 32), true)
	if !strings.Contains(leaky["detail"].(string), "SQLSTATE 42501") {
		t.Error("leak-internal variant should leak the original text")
	}
	nf := problemBody(ownErr("NOTE_ID_INVALID", map[string]string{"id": "abc"}), "en", "/x", "r", strings.Repeat("a", 32), false)
	if nf["detail"] != "abc is not a UUID" || nf["type"] != "urn:be:conformance/rawstub:NOTE_ID_INVALID" || nf["status"] != 400 {
		t.Errorf("own reason rendered wrong: %v", nf)
	}
	big := problemBody(beErr("BODY_TOO_LARGE", map[string]string{"limit": "1048576"}), "en", "/x", "r", strings.Repeat("a", 32), false)
	if big["status"] != 413 || big["code"] != "INVALID_ARGUMENT" {
		t.Errorf("BODY_TOO_LARGE: %v", big)
	}
}

var (
	reasonLine = regexp.MustCompile(`^\s*- reason: (\S+)`)
	fieldLine  = regexp.MustCompile(`^\s*(code|http): (\S+)`)
	textLine   = regexp.MustCompile(`^\s*(title|message):\s*\{ en: "(.*)", zh: "(.*)" \}`)
)

// parseCatalogue reads the flow-style rows of an errors.yaml file.
func parseCatalogue(t *testing.T, path string) map[string]reasonEntry {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]reasonEntry{}
	var cur string
	for _, line := range strings.Split(string(b), "\n") {
		if m := reasonLine.FindStringSubmatch(line); m != nil {
			cur = m[1]
			out[cur] = reasonEntry{}
			continue
		}
		e := out[cur]
		if m := fieldLine.FindStringSubmatch(line); m != nil && cur != "" {
			if m[1] == "code" {
				e.code = m[2]
			} else {
				e.http = atoi(m[2])
			}
		}
		if m := textLine.FindStringSubmatch(line); m != nil && cur != "" {
			if m[1] == "title" {
				e.titleEn, e.titleZh = m[2], m[3]
			} else {
				e.messageEn, e.msgZh = m[2], m[3]
			}
		}
		if cur != "" {
			out[cur] = e
		}
	}
	return out
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}

// TestCatalogues: every reason the stub can raise is catalogued with the same code, status
// and texts (P4.4, P4.7).
func TestCatalogues(t *testing.T) {
	for file, mine := range map[string]map[string]reasonEntry{"testdata/errors-be.yaml": beReasons, "contracts/errors.yaml": ownReasons} {
		cat := parseCatalogue(t, file)
		for r, e := range mine {
			if c, ok := cat[r]; !ok || c != e {
				t.Errorf("%s %s: stub %+v, catalogue %+v", file, r, e, c)
			}
		}
	}
}
