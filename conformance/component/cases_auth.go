package compconf

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/brickKit/be-acceptance/conformance/component/fakes"
)

// Token verification and the route decision chain (P5, P6.2).

// authOp is the protected route the auth cases use, with an allowed persona.
func (r *Run) authOp(id string) (Operation, string, bool) {
	if !r.needMain(id) {
		return Operation{}, "", false
	}
	op, ok := r.probeOp()
	if !ok {
		r.ev.notApplicable(id, "no protected route")
		return op, "", false
	}
	return op, r.personaFor(op), true
}

// expect401 checks a token is refused with 401 and the reason.
func (r *Run) expect401(ctx context.Context, id string, op Operation, what, tok, reason string) {
	x := r.call(ctx, op.Method, r.target(op), withHeader("Authorization", tok))
	r.ev.check(id, x.Status == 401 && x.reason() == reason, "%s: %s %s = %d %s, want 401 %s", what, op.Method, op.Path, x.Status, x.reason(), reason)
}

// expectAccepted checks a token passes verification and the guard (control case).
func (r *Run) expectAccepted(ctx context.Context, id string, op Operation, what, tok string) {
	x := r.call(ctx, op.Method, r.target(op), withToken(tok))
	r.ev.check(id, x.authorized(), "%s: %s %s = %d %s, want it accepted", what, op.Method, op.Path, x.Status, x.reason())
}

// CP-AUTH-01: no token, another scheme, or not a compact JWS answers 401 TOKEN_INVALID (P5.1).
func caseAuth01(ctx context.Context, r *Run) {
	const id = "CP-AUTH-01"
	op, p, ok := r.authOp(id)
	if !ok {
		return
	}
	x := r.call(ctx, op.Method, r.target(op))
	r.ev.check(id, x.Status == 401 && x.reason() == "TOKEN_INVALID", "no Authorization: %d %s, want 401 TOKEN_INVALID", x.Status, x.reason())
	r.expect401(ctx, id, op, "Basic scheme", "Basic Y29tcGNvbmY6eA==", "TOKEN_INVALID")
	r.expect401(ctx, id, op, "not a JWS", "Bearer not-a-jws", "TOKEN_INVALID")
	r.expect401(ctx, id, op, "raw token without Bearer", r.token(p), "TOKEN_INVALID")
	r.expectAccepted(ctx, id, op, "valid token (control)", r.token(p))
}

// CP-AUTH-02: alg none, HMAC, a missing kid, a header alg other than the key's, and a bad
// signature answer 401 (P5.2).
func caseAuth02(ctx context.Context, r *Run) {
	const id = "CP-AUTH-02"
	op, p, ok := r.authOp(id)
	if !ok {
		return
	}
	r.expect401(ctx, id, op, "alg none", "Bearer "+r.token(p, fakes.AlgNone()), "TOKEN_INVALID")
	r.expect401(ctx, id, op, "HS256", "Bearer "+r.token(p, fakes.HS256([]byte("compconf-secret"))), "TOKEN_INVALID")
	r.expect401(ctx, id, op, "no kid", "Bearer "+r.token(p, fakes.WithoutKid()), "TOKEN_INVALID")
	r.expect401(ctx, id, op, "header alg ES256 on an RS256 key", "Bearer "+r.token(p, fakes.WithHeader("alg", "ES256")), "TOKEN_INVALID")
	good := r.token(p)
	r.expect401(ctx, id, op, "bad signature", "Bearer "+good[:len(good)-6]+flip(good[len(good)-6:]), "TOKEN_INVALID")
}

func flip(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] == 'A' {
			b[i] = 'B'
		} else {
			b[i] = 'A'
		}
	}
	return string(b)
}

// CP-AUTH-03: a refresh token, a missing typ and a claim of the wrong type answer 401; an
// unknown claim is ignored (P5.3, P5.9).
func caseAuth03(ctx context.Context, r *Run) {
	const id = "CP-AUTH-03"
	op, p, ok := r.authOp(id)
	if !ok {
		return
	}
	r.expect401(ctx, id, op, "typ refresh", "Bearer "+r.token(p, fakes.WithClaim("typ", "refresh")), "TOKEN_INVALID")
	r.expect401(ctx, id, op, "refresh token shape (aud = issuer)", "Bearer "+r.token(p, fakes.WithClaim("typ", "refresh"),
		fakes.WithClaim("aud", []string{issuer}), fakes.WithClaim("sid", "s1")), "TOKEN_INVALID")
	r.expect401(ctx, id, op, "no typ", "Bearer "+r.token(p, fakes.WithoutClaim("typ")), "TOKEN_INVALID")
	r.expect401(ctx, id, op, "roles not an array", "Bearer "+r.token(p, fakes.WithClaim("roles", "compconf_all")), "TOKEN_INVALID")
	r.expectAccepted(ctx, id, op, "an unknown claim", r.token(p, fakes.WithClaim("compconf_unknown", map[string]any{"x": 1})))
}

// CP-AUTH-04: a wrong iss answers 401 (P5.3).
func caseAuth04(ctx context.Context, r *Run) {
	const id = "CP-AUTH-04"
	op, p, ok := r.authOp(id)
	if !ok {
		return
	}
	r.expect401(ctx, id, op, "wrong iss", "Bearer "+r.token(p, fakes.WithClaim("iss", "urn:be:other:iam")), "TOKEN_INVALID")
	r.expect401(ctx, id, op, "no iss", "Bearer "+r.token(p, fakes.WithoutClaim("iss")), "TOKEN_INVALID")
}

// CP-AUTH-05: a wrong aud answers 401; an array containing TENANT_ID is accepted (P5.3).
func caseAuth05(ctx context.Context, r *Run) {
	const id = "CP-AUTH-05"
	op, p, ok := r.authOp(id)
	if !ok {
		return
	}
	r.expect401(ctx, id, op, "wrong aud", "Bearer "+r.token(p, fakes.WithClaim("aud", "other-tenant")), "TOKEN_INVALID")
	r.expect401(ctx, id, op, "aud array without the tenant", "Bearer "+r.token(p, fakes.WithClaim("aud", []string{"a", "b"})), "TOKEN_INVALID")
	r.expectAccepted(ctx, id, op, "aud array containing the tenant", r.token(p, fakes.WithClaim("aud", []string{"other", tenantID})))
	r.expectAccepted(ctx, id, op, "aud string equal to the tenant", r.token(p, fakes.WithClaim("aud", tenantID)))
}

// CP-AUTH-06: a missing or expired exp answers 401; the 60 s skew is honoured (P5.3).
func caseAuth06(ctx context.Context, r *Run) {
	const id = "CP-AUTH-06"
	op, p, ok := r.authOp(id)
	if !ok {
		return
	}
	now := time.Now().Unix()
	r.expect401(ctx, id, op, "no exp", "Bearer "+r.token(p, fakes.WithoutClaim("exp")), "TOKEN_INVALID")
	r.expect401(ctx, id, op, "exp 120 s ago", "Bearer "+r.token(p, fakes.WithClaim("iat", now-700),
		fakes.WithClaim("nbf", now-700), fakes.WithClaim("exp", now-120)), "TOKEN_INVALID")
	r.expect401(ctx, id, op, "no jti", "Bearer "+r.token(p, fakes.WithoutClaim("jti")), "TOKEN_INVALID")
	r.expect401(ctx, id, op, "nbf 300 s ahead", "Bearer "+r.token(p, fakes.WithClaim("nbf", now+300)), "TOKEN_INVALID")
	r.expectAccepted(ctx, id, op, "exp 20 s ago, inside the 60 s skew", r.token(p, fakes.WithClaim("iat", now-600),
		fakes.WithClaim("nbf", now-600), fakes.WithClaim("exp", now-20)))
}

// CP-AUTH-07: an unknown kid triggers one JWKS refetch (a rotated key then verifies), at most
// once per 30 s; the previous key keeps verifying during the rotation (P5.4).
func caseAuth07(ctx context.Context, r *Run) {
	const id = "CP-AUTH-07"
	op, p, ok := r.authOp(id)
	if !ok {
		return
	}
	k2, err := r.iam.NewKey("ES256")
	if !r.ev.check(id, err == nil, "generating a key: %v", err) {
		return
	}
	r.iam.Publish(k2)
	before := r.iam.JWKSFetches()
	r.expectAccepted(ctx, id, op, "token signed with a key published after the JWKS was cached", r.token(p, fakes.SignedBy(k2)))
	r.ev.check(id, r.iam.JWKSFetches() > before, "the unknown kid did not trigger a JWKS fetch (P5.4)")
	r.expectAccepted(ctx, id, op, "token signed with the previous key during the rotation", r.token(p))
	before = r.iam.JWKSFetches()
	for i := 0; i < 4; i++ {
		k, _ := r.iam.NewKey("RS256")
		r.expect401(ctx, id, op, fmt.Sprintf("unknown kid %d", i+1), "Bearer "+r.token(p, fakes.SignedBy(k)), "TOKEN_INVALID")
	}
	r.ev.check(id, r.iam.JWKSFetches()-before <= 1, "%d JWKS fetches for 4 unknown kids within 30 s, want at most 1 (P5.4)", r.iam.JWKSFetches()-before)
}

// CP-AUTH-08: a token issued before stale_since[sub] − 5 s answers 401 TOKEN_STALE with
// WWW-Authenticate; a newer token works (P5.6).
func caseAuth08(ctx context.Context, r *Run) {
	const id = "CP-AUTH-08"
	op, _, ok := r.authOp(id)
	if !ok {
		return
	}
	now := time.Now().Unix()
	old := r.token(pStale, fakes.WithClaim("iat", now-120), fakes.WithClaim("nbf", now-120))
	r.authz.SetStale(r.personas[pStale].Sub, now-60)
	var x *exchange
	for deadline := time.Now().Add(25 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
		x = r.call(ctx, op.Method, r.target(op), withToken(old))
		if x.Status == 401 {
			break
		}
	}
	r.ev.check(id, x.Status == 401 && x.reason() == "TOKEN_STALE", "stale token within 25 s of the bundle change: %d %s, want 401 TOKEN_STALE", x.Status, x.reason())
	r.ev.check(id, strings.Contains(x.Header.Get("WWW-Authenticate"), `error="token_stale"`),
		"WWW-Authenticate %q lacks error=\"token_stale\"", x.Header.Get("WWW-Authenticate"))
	r.expectAccepted(ctx, id, op, "a token issued after stale_since", r.token(pStale))
}

// CP-AUTH-09: every OpenAPI operation declares a guard; without a token every non-Public one
// answers 401 and every Public one serves (P6.2).
func caseAuth09(ctx context.Context, r *Run) {
	const id = "CP-AUTH-09"
	if !r.needMain(id) {
		return
	}
	for _, op := range r.comp.Operations {
		if !r.ev.check(id, op.Guard != "", "%s %s declares no x-be-permission", op.Method, op.Path) {
			continue
		}
		var body []reqOpt
		if op.Method != "GET" && op.Method != "DELETE" && op.Method != "HEAD" {
			body = append(body, withBody([]byte("{}")))
		}
		x := r.call(ctx, op.Method, r.target(op), body...)
		if op.Guard == GuardPublic {
			r.ev.check(id, x.Status != 401, "Public %s %s answered 401 without a token", op.Method, op.Path)
		} else {
			r.ev.check(id, x.Status == 401 && x.reason() == "TOKEN_INVALID", "%s %s without a token = %d %s, want 401 TOKEN_INVALID", op.Method, op.Path, x.Status, x.reason())
		}
	}
}

// CP-AUTH-11: a caller without the route's key answers 403 MISSING_PERMISSION with
// metadata.permission (P6.2).
func caseAuth11(ctx context.Context, r *Run) {
	const id = "CP-AUTH-11"
	if !r.needMain(id) {
		return
	}
	n := 0
	for _, op := range r.comp.Operations {
		if !op.KeyGuarded() {
			continue
		}
		n++
		var body []reqOpt
		if op.Method != "GET" && op.Method != "DELETE" {
			body = append(body, withBody([]byte("{}")))
		}
		x := r.call(ctx, op.Method, r.target(op), append(body, withToken(r.token(pNone)))...)
		r.ev.check(id, x.Status == 403 && x.reason() == "MISSING_PERMISSION" && x.metadata("permission") == op.Guard,
			"%s %s without %s = %d %s permission=%q, want 403 MISSING_PERMISSION", op.Method, op.Path, op.Guard, x.Status, x.reason(), x.metadata("permission"))
	}
	if n == 0 {
		r.ev.notApplicable(id, "no route guarded by a permission key")
	}
}

// CP-AUTH-12: act.kind = agent while the bundle's agents is false, and ceil / dg while
// delegation is false, answer 401 UNSUPPORTED_DELEGATION (P5.5).
func caseAuth12(ctx context.Context, r *Run) {
	const id = "CP-AUTH-12"
	op, p, ok := r.authOp(id)
	if !ok {
		return
	}
	agent := map[string]any{"sub": "agent-compconf", "kind": "agent"}
	r.expect401(ctx, id, op, "act.kind agent", "Bearer "+r.token(p, fakes.WithClaim("act", agent)), "UNSUPPORTED_DELEGATION")
	nested := map[string]any{"sub": uuidv7(), "kind": "user", "act": agent}
	r.expect401(ctx, id, op, "agent deeper in the act chain", "Bearer "+r.token(p, fakes.WithClaim("act", nested)), "UNSUPPORTED_DELEGATION")
	r.expect401(ctx, id, op, "ceil and dg with delegation false", "Bearer "+r.token(p, fakes.WithClaim("act", map[string]any{"sub": uuidv7(), "kind": "user"}),
		fakes.WithClaim("ceil", []string{"view_as_ro"}), fakes.WithClaim("dg", "dg_1")), "UNSUPPORTED_DELEGATION")
}
