package main

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type staticKeys map[string]jwk

func (s staticKeys) lookup(_ context.Context, kid string) (jwk, bool) {
	k, ok := s[kid]
	return k, ok
}

type signers struct {
	rsa *rsa.PrivateKey
	ec  *ecdsa.PrivateKey
	ed  ed25519.PrivateKey
}

func newSigners(t *testing.T) (signers, staticKeys) {
	t.Helper()
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	ek, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, dk, _ := ed25519.GenerateKey(rand.Reader)
	return signers{rk, ek, dk}, staticKeys{
		"r1": {alg: "RS256", key: &rk.PublicKey},
		"e1": {alg: "ES256", key: &ek.PublicKey},
		"d1": {alg: "EdDSA", key: dk.Public()},
	}
}

func b64(v any) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s signers) sign(t *testing.T, hdr, claims map[string]any) string {
	t.Helper()
	in := b64(hdr) + "." + b64(claims)
	h := sha256.Sum256([]byte(in))
	var sig []byte
	switch hdr["alg"] {
	case "RS256":
		sig, _ = rsa.SignPKCS1v15(rand.Reader, s.rsa, crypto.SHA256, h[:])
	case "ES256":
		r, ss, _ := ecdsa.Sign(rand.Reader, s.ec, h[:])
		sig = append(r.FillBytes(make([]byte, 32)), ss.FillBytes(make([]byte, 32))...)
	case "EdDSA":
		sig = ed25519.Sign(s.ed, []byte(in))
	case "HS256":
		m := hmac.New(sha256.New, []byte("secret"))
		m.Write([]byte(in))
		sig = m.Sum(nil)
	}
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}

var testNow = time.Unix(1_800_000_000, 0)

func goodClaims() map[string]any {
	return map[string]any{
		"iss": "urn:be:t1:iam", "aud": "t1", "typ": "access", "sub": "u-1", "jti": "j-1",
		"iat": testNow.Unix() - 10, "exp": testNow.Unix() + 600, "roles": []string{"rawstub_editor"},
		"dept_path": "/1/3/",
	}
}

func with(m map[string]any, k string, v any) map[string]any {
	out := map[string]any{}
	for kk, vv := range m {
		out[kk] = vv
	}
	if v == nil {
		delete(out, k)
	} else {
		out[k] = v
	}
	return out
}

func TestVerifyTokens(t *testing.T) {
	s, keys := newSigners(t)
	v := &tokenVerifier{keys: keys, issuer: "urn:be:t1:iam", tenant: "t1", now: func() time.Time { return testNow }}
	rs := map[string]any{"alg": "RS256", "kid": "r1", "typ": "JWT"}
	gc := goodClaims()
	cases := []struct {
		name   string
		header string
		ok     bool
	}{
		{"valid RS256", "Bearer " + s.sign(t, rs, gc), true},
		{"valid ES256", "Bearer " + s.sign(t, map[string]any{"alg": "ES256", "kid": "e1"}, gc), true},
		{"valid EdDSA", "Bearer " + s.sign(t, map[string]any{"alg": "EdDSA", "kid": "d1"}, gc), true},
		{"lower-case scheme", "bearer " + s.sign(t, rs, gc), true},
		{"aud array", "Bearer " + s.sign(t, rs, with(gc, "aud", []any{"x", "t1"})), true},
		{"exp within skew", "Bearer " + s.sign(t, rs, with(gc, "exp", testNow.Unix()-30)), true},
		{"svc sub", "Bearer " + s.sign(t, rs, with(gc, "sub", "svc:billing")), true},
		{"unknown claim ignored", "Bearer " + s.sign(t, rs, with(gc, "x_extra", 5)), true},
		{"no header", "", false},
		{"basic scheme", "Basic dTpw", false},
		{"not a JWS", "Bearer abc.def", false},
		{"alg none", "Bearer " + b64(map[string]any{"alg": "none", "kid": "r1"}) + "." + b64(gc) + ".", false},
		{"HS256", "Bearer " + s.sign(t, map[string]any{"alg": "HS256", "kid": "r1"}, gc), false},
		{"missing kid", "Bearer " + s.sign(t, map[string]any{"alg": "RS256"}, gc), false},
		{"unknown kid", "Bearer " + s.sign(t, map[string]any{"alg": "RS256", "kid": "zz"}, gc), false},
		{"alg differs from key", "Bearer " + s.sign(t, map[string]any{"alg": "ES256", "kid": "r1"}, gc), false},
		{"bad signature", "Bearer " + s.sign(t, rs, gc)[:20] + "x" + s.sign(t, rs, gc)[21:], false},
		{"refresh", "Bearer " + s.sign(t, rs, with(gc, "typ", "refresh")), false},
		{"typ missing", "Bearer " + s.sign(t, rs, with(gc, "typ", nil)), false},
		{"wrong iss", "Bearer " + s.sign(t, rs, with(gc, "iss", "urn:be:other:iam")), false},
		{"wrong aud", "Bearer " + s.sign(t, rs, with(gc, "aud", "t2")), false},
		{"aud array non-string", "Bearer " + s.sign(t, rs, with(gc, "aud", []any{"t1", 3})), false},
		{"sub empty", "Bearer " + s.sign(t, rs, with(gc, "sub", "")), false},
		{"exp missing", "Bearer " + s.sign(t, rs, with(gc, "exp", nil)), false},
		{"expired", "Bearer " + s.sign(t, rs, with(gc, "exp", testNow.Unix()-61)), false},
		{"nbf future", "Bearer " + s.sign(t, rs, with(gc, "nbf", testNow.Unix()+120)), false},
		{"iat future", "Bearer " + s.sign(t, rs, with(gc, "iat", testNow.Unix()+120)), false},
		{"iat missing", "Bearer " + s.sign(t, rs, with(gc, "iat", nil)), false},
		{"jti missing", "Bearer " + s.sign(t, rs, with(gc, "jti", nil)), false},
		{"roles not array", "Bearer " + s.sign(t, rs, with(gc, "roles", "admin")), false},
		{"roles non-string", "Bearer " + s.sign(t, rs, with(gc, "roles", []any{1})), false},
		{"dept_path not string", "Bearer " + s.sign(t, rs, with(gc, "dept_path", 7)), false},
		{"act bad kind", "Bearer " + s.sign(t, rs, with(gc, "act", map[string]any{"sub": "a", "kind": "robot"})), false},
		{"act nested bad", "Bearer " + s.sign(t, rs, with(gc, "act", map[string]any{"sub": "a", "kind": "user", "act": map[string]any{"kind": "svc"}})), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cl, err := v.verify(context.Background(), c.header)
			if c.ok && err != nil {
				t.Fatalf("refused: %v", err)
			}
			if !c.ok && (err == nil || !errors.Is(err, errToken)) {
				t.Fatalf("accepted, want TOKEN_INVALID (claims %+v)", cl)
			}
		})
	}
	v.acceptRefresh = true
	if _, err := v.verify(context.Background(), "Bearer "+s.sign(t, rs, with(gc, "typ", "refresh"))); err != nil {
		t.Errorf("accept-refresh variant should accept a refresh token: %v", err)
	}
}

func TestTokenChecksAndKeys(t *testing.T) {
	var b Bundle
	raw := `{"contract":"authz/2.0","capabilities":{"core":true,"delegation":true,"agents":false},
		"roles":{"rawstub_editor":["conformance.rawstub.view"],"old":["conformance.rawstub.create"]},
		"grants":{"old":{"until":100}},"stale_since":{"u-stale":1000},"revoked_grants":{"dg_1":5}}`
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		c      Claims
		reason string
	}{
		{Claims{Sub: "u-1", Iat: 10}, ""},
		{Claims{Sub: "u-stale", Iat: 994}, "TOKEN_STALE"},
		{Claims{Sub: "u-stale", Iat: 995}, ""},
		{Claims{Sub: "u-1", Dg: "dg_1"}, "TOKEN_STALE"},
		{Claims{Sub: "u-1", Act: &Actor{Sub: "bot", Kind: "agent"}}, "UNSUPPORTED_DELEGATION"},
		{Claims{Sub: "u-1", Act: &Actor{Sub: "s", Kind: "svc"}}, ""},
	}
	for i, c := range cases {
		if r, _ := tokenChecks(&b, &c.c); r != c.reason {
			t.Errorf("case %d: got %q want %q", i, r, c.reason)
		}
	}
	alice := &Claims{Sub: "a", Roles: []string{"rawstub_editor", "old"}}
	if !hasKey(&b, alice, "conformance.rawstub.view", 500) || hasKey(&b, alice, "conformance.rawstub.create", 500) {
		t.Error("role keys or grant window evaluated wrongly")
	}
	if hasKey(&b, &Claims{Sub: "n"}, "conformance.rawstub.view", 500) {
		t.Error("no roles must hold no key")
	}
	ceiled := &Claims{Sub: "a", Roles: []string{"rawstub_editor"}, Ceil: []string{"unknown"}}
	if hasKey(&b, ceiled, "conformance.rawstub.view", 500) {
		t.Error("an unknown ceiling is an empty profile")
	}
}
