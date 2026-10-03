package compconf

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/brickKit/be-acceptance/conformance/component/protoschema"
)

// CP-CORE-07: the image has /bin/sh and wget (P1.9).
func caseCore07(ctx context.Context, r *Run) {
	const id = "CP-CORE-07"
	if !r.needMain(id) {
		return
	}
	out, code, err := r.main.Exec(ctx, "/bin/sh", "-c", "command -v wget")
	r.ev.check(id, err == nil && code == 0 && strings.TrimSpace(out) != "", "/bin/sh -c 'command -v wget' = %d %q %v (P1.9)", code, out, err)
}

// CP-CORE-13: /healthz answers on 127.0.0.1 and ::1 inside the container; every declared
// port accepts IPv4 and IPv6 connections (P1.13).
func caseCore13(ctx context.Context, r *Run) {
	const id = "CP-CORE-13"
	if !r.needMain(id) {
		return
	}
	main := r.comp.Manifest.Deployment.Port
	for _, host := range []string{"127.0.0.1", "[::1]"} {
		u := fmt.Sprintf("http://%s:%d/healthz", host, main)
		out, code, err := r.main.Exec(ctx, "wget", "-q", "-T", "3", "-O", "/dev/null", u)
		r.ev.check(id, err == nil && code == 0, "wget %s inside the container = %d %s (P1.13)", u, code, strings.TrimSpace(out))
	}
	for _, p := range r.comp.Manifest.Deployment.ExtraPorts {
		for _, host := range []string{"127.0.0.1", "[::1]"} {
			u := fmt.Sprintf("http://%s:%d/", host, p.Port)
			out, _, _ := r.main.Exec(ctx, "wget", "-q", "-T", "3", "-O", "/dev/null", u)
			refused := strings.Contains(out, "refused") || strings.Contains(out, "can't connect")
			r.ev.check(id, !refused, "port %s (%d) refuses %s inside the container: %s (P1.13)", p.Name, p.Port, host, strings.TrimSpace(out))
		}
	}
}

// CP-CORE-14 (runtime): no secret value in the container's environment; each secret key
// holds the path of its mounted file (P2.7).
func caseCore14Runtime(ctx context.Context, r *Run) {
	const id = "CP-CORE-14"
	if !r.needMain(id) {
		return
	}
	env, err := r.main.InspectEnv(ctx)
	if !r.ev.check(id, err == nil, "docker inspect: %v", err) {
		return
	}
	for _, kv := range env {
		for k, v := range r.secrets {
			r.ev.check(id, len(v) < 4 || !strings.Contains(kv, v), "the value of %s appears in the environment (P2.7)", k)
		}
	}
	for k := range r.secrets {
		want := k + "=" + SecretsRoot + "/" + r.comp.ServiceName() + "/" + k
		r.ev.check(id, contains(env, want), "%s is not the path of its mounted file", k)
	}
}

// CP-CORE-10: X-Request-Id is kept when sent, the trace ID when absent, and always answered.
func caseCore10(ctx context.Context, r *Run) {
	const id = "CP-CORE-10"
	if !r.needMain(id) {
		return
	}
	target := "/_be/info"
	if op, ok := r.publicOp(); ok {
		target = r.target(op)
	}
	x := r.call(ctx, "GET", target, withHeader("X-Request-Id", "compconf-rid-1"))
	r.ev.check(id, x.Header.Get("X-Request-Id") == "compconf-rid-1", "sent X-Request-Id compconf-rid-1, got %q (P3.2)", x.Header.Get("X-Request-Id"))
	tid := strings.ReplaceAll(uuidv7(), "-", "")
	x = send(ctx, r.rec, r.base, "GET", target, withHeader("traceparent", "00-"+tid+"-00f067aa0ba902b7-01"))
	r.ev.check(id, x.Header.Get("X-Request-Id") == tid, "without X-Request-Id the trace ID %s is the request ID, got %q (P3.2)", tid, x.Header.Get("X-Request-Id"))
	x = send(ctx, r.rec, r.base, "GET", target)
	r.ev.check(id, x.Header.Get("X-Request-Id") != "", "no X-Request-Id generated (P3.2)")
	if op, ok := r.probeOp(); ok {
		x = send(ctx, r.rec, r.base, op.Method, r.target(op))
		r.ev.check(id, x.Header.Get("X-Request-Id") != "" && x.str("request_id") == x.Header.Get("X-Request-Id"),
			"a 401 answer carries X-Request-Id %q and request_id %q (P3.2)", x.Header.Get("X-Request-Id"), x.str("request_id"))
	}
}

// CP-CORE-11: /_be/info matches info.schema.json, the manifests and the container; the main
// port serves no path outside the three kinds (P3.1).
func caseCore11(ctx context.Context, r *Run) {
	const id = "CP-CORE-11"
	if !r.needMain(id) {
		return
	}
	x := r.call(ctx, "GET", "/_be/info")
	if !r.ev.check(id, x.Status == 200, "/_be/info = %d (P20.4)", x.Status) {
		return
	}
	var info map[string]any
	if !r.ev.check(id, json.Unmarshal(x.Body, &info) == nil, "/_be/info is not JSON") {
		return
	}
	r.lastInfo = info
	if s, err := protoschema.ProtocolSchema("info.schema.json"); err == nil {
		err = protoschema.ValidateJSON(s, x.Body)
		r.ev.check(id, err == nil, "/_be/info does not match info.schema.json: %v", err)
	}
	str := func(k string) string { s, _ := info[k].(string); return s }
	r.ev.check(id, str("component_id") == r.comp.ID() && str("component_version") == r.comp.Version(),
		"component_id/version %q %q, want %q %q", str("component_id"), str("component_version"), r.comp.ID(), r.comp.Version())
	r.ev.check(id, str("protocol") == r.comp.Assembly.Protocol, "protocol %q, assembly.yaml declares %q (P20.2)", str("protocol"), r.comp.Assembly.Protocol)
	r.checkInfoPorts(info)
	r.checkInfoProfiles(info)
	lang, _ := info["language"].(map[string]any)
	if info["sdk"] == nil {
		r.ev.check(id, r.comp.Assembly.Language != "", "sdk is null but assembly.yaml declares no language (P20.2)")
	}
	if r.comp.Assembly.Language != "" {
		r.ev.check(id, lang["name"] == r.comp.Assembly.Language, "language.name %v, assembly.yaml declares %q", lang["name"], r.comp.Assembly.Language)
	}
	r.ev.check(id, info["members"] == nil, "members of a component must be null")
	for k, v := range r.secrets {
		r.ev.check(id, len(v) < 4 || !strings.Contains(string(x.Body), v), "/_be/info carries the value of %s (P20.4)", k)
	}
	y := r.call(ctx, "GET", "/compconf-not-a-path/"+uuidv7())
	r.ev.check(id, y.Status == 404, "a path outside the three kinds answered %d, want 404 (P3.1)", y.Status)
}

func (r *Run) checkInfoPorts(info map[string]any) {
	const id = "CP-CORE-11"
	ports, _ := info["ports"].(map[string]any)
	want := map[string]float64{"http": float64(r.comp.Manifest.Deployment.Port)}
	for _, p := range r.comp.Manifest.Deployment.ExtraPorts {
		want[p.Name] = float64(p.Port)
	}
	for name, p := range want {
		r.ev.check(id, ports[name] == p, "ports.%s = %v, component.yaml says %v (P20.4)", name, ports[name], p)
	}
	r.ev.check(id, len(ports) == len(want), "ports %v has names component.yaml does not declare", ports)
}

func (r *Run) checkInfoProfiles(info map[string]any) {
	var claimed []string
	if ps, ok := info["profiles"].([]any); ok {
		for _, p := range ps {
			claimed = append(claimed, fmt.Sprint(p))
		}
	}
	sort.Strings(claimed)
	sel := append([]string(nil), r.selected...)
	sort.Strings(sel)
	r.ev.check("CP-CORE-11", strings.Join(claimed, ",") == strings.Join(sel, ","),
		"profiles claimed in /_be/info %v differ from those the manifests select %v (P20)", claimed, sel)
}

// CP-CORE-09: a body above the route's limit answers 413 BODY_TOO_LARGE (P3.6, P3.13).
func caseCore09(ctx context.Context, r *Run) {
	const id = "CP-CORE-09"
	if !r.needMain(id) {
		return
	}
	op, ok := r.bodyOp()
	if !ok {
		r.ev.notApplicable(id, "no route takes a body")
		return
	}
	limit := op.MaxBodyBytes
	if limit == 0 {
		limit = 1 << 20
	}
	body := []byte(`{"pad":"` + strings.Repeat("a", limit) + `"}`)
	x := r.call(ctx, op.Method, r.target(op), withToken(r.token(r.personaFor(op))), withBody(body))
	r.ev.check(id, x.Status == 413 && x.reason() == "BODY_TOO_LARGE",
		"%s %s with %d bytes (limit %d) = %d %s, want 413 BODY_TOO_LARGE", op.Method, op.Path, len(body), limit, x.Status, x.reason())
}

// CP-CORE-08: a client that sends headers slowly is cut within 6 s (P3.5).
func caseCore08(ctx context.Context, r *Run) {
	const id = "CP-CORE-08"
	if !r.needMain(id) {
		return
	}
	addr := strings.TrimPrefix(r.base, "http://")
	c, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if !r.ev.check(id, err == nil, "connect: %v", err) {
		return
	}
	defer c.Close()
	start := time.Now()
	_, _ = io.WriteString(c, "GET /healthz HTTP/1.1\r\nHost: compconf\r\n")
	buf := make([]byte, 512)
	for time.Since(start) < 10*time.Second {
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := c.Read(buf); err == nil || !isTimeout(err) {
			break // the server answered (408) or closed
		}
		if _, err := io.WriteString(c, "X-Compconf-Slow: "+strconv.Itoa(int(time.Since(start).Seconds()))+"\r\n"); err != nil {
			break
		}
	}
	took := time.Since(start)
	r.ev.check(id, took <= 7*time.Second, "a client sending headers slowly was cut after %v, want within 6 s (+1 s tolerance) (P3.5)", took.Round(100*time.Millisecond))
}

func isTimeout(err error) bool {
	ne, ok := err.(net.Error)
	return ok && ne.Timeout()
}
