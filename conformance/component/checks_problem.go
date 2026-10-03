package compconf

import (
	"fmt"
	"io/fs"
	"strings"

	"github.com/brickKit/be-acceptance/conformance/component/protoschema"
	beprotocol "github.com/brickKit/be-protocol"
	"gopkg.in/yaml.v3"
)

// catalogs are the reason catalogues a component's errors are checked against.
type catalogs struct {
	be  ErrorCatalog // be-protocol schemas/errors-be.yaml
	own ErrorCatalog // the component's contracts/errors.yaml
}

func loadCatalogs(c *Component) (catalogs, error) {
	var cats catalogs
	b, err := fs.ReadFile(beprotocol.FS, "schemas/errors-be.yaml")
	if err != nil {
		return cats, err
	}
	if err := yaml.Unmarshal(b, &cats.be); err != nil {
		return cats, err
	}
	cats.own = c.Errors
	return cats, nil
}

// httpOfCode is the REST status of each gRPC code (be-protocol P4, code mapping).
var httpOfCode = map[string]int{
	"INVALID_ARGUMENT": 400, "FAILED_PRECONDITION": 400, "OUT_OF_RANGE": 400, "UNAUTHENTICATED": 401,
	"PERMISSION_DENIED": 403, "NOT_FOUND": 404, "ALREADY_EXISTS": 409, "ABORTED": 409,
	"RESOURCE_EXHAUSTED": 429, "CANCELLED": 499, "UNIMPLEMENTED": 501, "UNAVAILABLE": 503,
	"DEADLINE_EXCEEDED": 504, "INTERNAL": 500, "UNKNOWN": 500, "DATA_LOSS": 500,
}

// reasonIssues checks that a (domain, reason) pair is catalogued (P4.4, P4.7).
func reasonIssues(domain, reason, compID string, cats catalogs) []string {
	switch domain {
	case "be":
		if !cats.be.Has(reason) {
			return []string{fmt.Sprintf("reason %s of domain be is not in errors-be.yaml (P4.7)", reason)}
		}
	case compID:
		if cats.be.Has(reason) {
			return []string{fmt.Sprintf("reserved reason %s raised in the component's own domain (P4.7)", reason)}
		}
		if !cats.own.Has(reason) {
			return []string{fmt.Sprintf("reason %s is not in contracts/errors.yaml (P4.4)", reason)}
		}
	default: // relayed from a dependency (P4.4): its catalogue is not in hand
	}
	return nil
}

// problemIssues checks one 4xx/5xx answer against P4.1, P4.8 and the catalogues.
func problemIssues(x *exchange, compID string, cats catalogs) []string {
	var is []string
	add := func(f string, a ...any) {
		is = append(is, fmt.Sprintf("%s %s → %d: ", x.Method, x.Path, x.Status)+fmt.Sprintf(f, a...))
	}
	if ct := x.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		add("Content-Type %q, want application/problem+json", ct)
	}
	if x.Problem == nil {
		add("body is not a JSON object")
		return is
	}
	s, err := protoschema.ProtocolSchema("problem.schema.json")
	if err == nil {
		if err := protoschema.ValidateValue(s, x.Problem); err != nil {
			add("body does not match problem.schema.json: %v", err)
		}
	}
	reason, domain, code := x.reason(), x.str("domain"), x.str("code")
	if st, _ := x.Problem["status"].(float64); int(st) != x.Status {
		add("body status %v differs from the HTTP status", x.Problem["status"])
	}
	want := httpOfCode[code]
	if reason == "BODY_TOO_LARGE" {
		want = 413
	}
	if code != "" && want != x.Status && !(reason == "UPSTREAM_UNAVAILABLE" && x.Status == 502) {
		add("code %s maps to HTTP %d (P4 code mapping)", code, want)
	}
	if t := x.str("type"); t != "urn:be:"+domain+":"+reason {
		add("type %q, want urn:be:%s:%s", t, domain, reason)
	}
	if inst := x.str("instance"); inst != x.Path {
		add("instance %q, want the request path %q", inst, x.Path)
	}
	if rid := x.str("request_id"); rid != x.Header.Get("X-Request-Id") {
		add("request_id %q differs from X-Request-Id %q", rid, x.Header.Get("X-Request-Id"))
	}
	for _, r := range reasonIssues(domain, reason, compID, cats) {
		add("%s", r)
	}
	return is
}

// leakMarkers are fragments of database and driver error text that never reach a caller (P4.3).
var leakMarkers = []string{"sqlstate", "42501", "permission denied", "error:", "pgx", "pq:", "select ", "insert ",
	"update ", "delete ", "relation ", "violates", "syntax", "goroutine", "panic", "stack", ".go:"}

// leakIssues looks for SQL or driver text, and for the given identifiers (table, schema and
// role names), in every member of an INTERNAL problem except instance, type and the IDs.
func leakIssues(p map[string]any, idents []string) []string {
	var is []string
	for k, v := range p {
		switch k {
		case "instance", "type", "request_id", "trace_id", "reason", "domain", "code", "status":
			continue
		}
		text := strings.ToLower(fmt.Sprint(v))
		for _, m := range leakMarkers {
			if strings.Contains(text, m) {
				is = append(is, fmt.Sprintf("%s carries %q (P4.3)", k, m))
			}
		}
		for _, id := range idents {
			if id != "" && strings.Contains(text, strings.ToLower(id)) {
				is = append(is, fmt.Sprintf("%s names %q (P4.3)", k, id))
			}
		}
	}
	return is
}
