package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"strings"
	"time"
)

// Claims are the verified access-token claims a component reads (P5.5).
type Claims struct {
	Sub, TenantID, DeptPath, Dg, Azp, Locale, Jti string
	Roles, Ceil                                   []string
	Act                                           *Actor
	Iat                                           int64
}

// Actor is the act claim, nestable.
type Actor struct {
	Sub, Kind string
	Act       *Actor
}

// keySource finds the JWKS key for a kid.
type keySource interface {
	lookup(ctx context.Context, kid string) (jwk, bool)
}

// tokenVerifier implements contract-infra-iam TOKENS.md "Verification", checks 1–12; checks
// 13 and 14 need the bundle and live in the route guard.
type tokenVerifier struct {
	keys          keySource
	issuer        string
	tenant        string
	now           func() time.Time
	acceptRefresh bool // broken variant accept-refresh
}

const skew = 60

var errToken = errors.New("token invalid")

func tokenFail(why string) error { return errors.Join(errToken, errors.New(why)) }

// verify returns the claims, or an error wrapping errToken whose text names the failed check
// (for the log only, never the response).
func (v *tokenVerifier) verify(ctx context.Context, authz string) (*Claims, error) {
	const prefix = "bearer "
	if len(authz) < len(prefix) || !strings.EqualFold(authz[:len(prefix)], prefix) {
		return nil, tokenFail("no bearer token")
	}
	parts := strings.Split(strings.TrimSpace(authz[len(prefix):]), ".")
	if len(parts) != 3 {
		return nil, tokenFail("not a compact JWS")
	}
	hdrRaw, err1 := base64.RawURLEncoding.DecodeString(parts[0])
	payRaw, err2 := base64.RawURLEncoding.DecodeString(parts[1])
	sig, err3 := base64.RawURLEncoding.DecodeString(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return nil, tokenFail("bad base64url")
	}
	var hdr map[string]any
	var pay map[string]any
	if err := decodeObject(hdrRaw, &hdr); err != nil {
		return nil, tokenFail("header is not a JSON object")
	}
	if err := decodeObject(payRaw, &pay); err != nil {
		return nil, tokenFail("payload is not a JSON object")
	}
	alg, _ := hdr["alg"].(string)
	if alg != "RS256" && alg != "ES256" && alg != "EdDSA" {
		return nil, tokenFail("alg not allowed")
	}
	kid, _ := hdr["kid"].(string)
	if kid == "" {
		return nil, tokenFail("kid missing")
	}
	key, ok := v.keys.lookup(ctx, kid)
	if !ok {
		return nil, tokenFail("unknown kid")
	}
	if key.alg != alg {
		return nil, tokenFail("alg differs from the key's")
	}
	if !verifySig(alg, key.key, []byte(parts[0]+"."+parts[1]), sig) {
		return nil, tokenFail("bad signature")
	}
	return v.checkClaims(pay)
}

func decodeObject(b []byte, out *map[string]any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if *out == nil {
		return errors.New("null")
	}
	return nil
}

func verifySig(alg string, key crypto.PublicKey, signed, sig []byte) bool {
	h := sha256.Sum256(signed)
	switch alg {
	case "RS256":
		pk, ok := key.(*rsa.PublicKey)
		return ok && rsa.VerifyPKCS1v15(pk, crypto.SHA256, h[:], sig) == nil
	case "ES256":
		pk, ok := key.(*ecdsa.PublicKey)
		if !ok || len(sig) != 64 {
			return false
		}
		r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
		return ecdsa.Verify(pk, h[:], r, s)
	case "EdDSA":
		pk, ok := key.(ed25519.PublicKey)
		return ok && ed25519.Verify(pk, signed, sig)
	}
	return false
}

// checkClaims is TOKENS.md checks 4–12, in order.
func (v *tokenVerifier) checkClaims(p map[string]any) (*Claims, error) {
	c := &Claims{}
	var ok bool
	if c.Roles, ok = strArray(p, "roles"); !ok {
		return nil, tokenFail("roles is not an array of strings")
	}
	if c.Ceil, ok = strArray(p, "ceil"); !ok {
		return nil, tokenFail("ceil is not an array of strings")
	}
	for name, dst := range map[string]*string{"dept_path": &c.DeptPath, "tenant_id": &c.TenantID, "azp": &c.Azp, "locale": &c.Locale, "dg": &c.Dg} {
		if *dst, ok = optString(p, name); !ok {
			return nil, tokenFail(name + " is not a string")
		}
	}
	if raw, present := p["act"]; present {
		if c.Act, ok = parseActor(raw); !ok {
			return nil, tokenFail("act is malformed")
		}
	}
	if iss, _ := p["iss"].(string); iss == "" || iss != v.issuer {
		return nil, tokenFail("iss mismatch")
	}
	if !audContains(p["aud"], v.tenant) {
		return nil, tokenFail("aud does not contain the tenant")
	}
	typ, _ := p["typ"].(string)
	if typ != "access" && !(v.acceptRefresh && typ == "refresh") {
		return nil, tokenFail("typ is not access")
	}
	if c.Sub, _ = p["sub"].(string); c.Sub == "" {
		return nil, tokenFail("sub missing")
	}
	now := float64(v.now().Unix())
	exp, ok := number(p, "exp")
	if !ok || !(now < exp+skew) {
		return nil, tokenFail("exp missing or passed")
	}
	if _, present := p["nbf"]; present {
		nbf, ok := number(p, "nbf")
		if !ok || nbf > now+skew {
			return nil, tokenFail("nbf in the future")
		}
	}
	iat, ok := number(p, "iat")
	if !ok || iat > now+skew {
		return nil, tokenFail("iat missing or in the future")
	}
	c.Iat = int64(iat)
	if c.Jti, _ = p["jti"].(string); c.Jti == "" {
		return nil, tokenFail("jti missing")
	}
	return c, nil
}

func strArray(p map[string]any, name string) ([]string, bool) {
	raw, present := p[name]
	if !present {
		return nil, true
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		s, ok := x.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func optString(p map[string]any, name string) (string, bool) {
	raw, present := p[name]
	if !present {
		return "", true
	}
	s, ok := raw.(string)
	return s, ok
}

func parseActor(raw any) (*Actor, bool) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}
	a := &Actor{}
	a.Sub, _ = m["sub"].(string)
	a.Kind, _ = m["kind"].(string)
	if a.Sub == "" || (a.Kind != "user" && a.Kind != "agent" && a.Kind != "svc") {
		return nil, false
	}
	if inner, present := m["act"]; present {
		if a.Act, ok = parseActor(inner); !ok {
			return nil, false
		}
	}
	return a, true
}

func audContains(raw any, tenant string) bool {
	switch t := raw.(type) {
	case string:
		return t != "" && t == tenant
	case []any:
		found := false
		for _, x := range t {
			s, ok := x.(string)
			if !ok {
				return false
			}
			found = found || s == tenant
		}
		return found
	}
	return false
}

func number(p map[string]any, name string) (float64, bool) {
	n, ok := p[name].(json.Number)
	if !ok {
		return 0, false
	}
	f, err := n.Float64()
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, false
	}
	return f, true
}
