package fakes

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// IAM is fake-iam: an identity provider per contract-infra-iam iam/1 that publishes
// discovery metadata and a JWKS under IAM_URL and mints platform access tokens, valid or
// deliberately wrong, for the suite's personas.
type IAM struct {
	Issuer    string // IAM_ISSUER
	Tenant    string // TENANT_ID
	mu        sync.Mutex
	current   *SigningKey
	published []*SigningKey
	fetches   atomic.Int64
	baseURL   string
}

// Persona is the subject of a minted token.
type Persona struct {
	Sub      string
	Roles    []string
	DeptPath *string // nil or "" = no department: the claim is omitted (TOKENS.md)
	Act      map[string]any
	Locale   string
}

// NewIAM makes a provider with one RS256 signing key.
func NewIAM(issuer, tenant string) (*IAM, error) {
	k, err := GenerateKey("RS256")
	if err != nil {
		return nil, err
	}
	return &IAM{Issuer: issuer, Tenant: tenant, current: k, published: []*SigningKey{k}}, nil
}

// SetBaseURL is IAM_URL as components see it (used in the discovery document).
func (f *IAM) SetBaseURL(u string) { f.mu.Lock(); f.baseURL = u; f.mu.Unlock() }

// NewKey generates a key that is not yet published.
func (f *IAM) NewKey(alg string) (*SigningKey, error) { return GenerateKey(alg) }

// Publish adds a key to the JWKS (at most 3 keys are kept, oldest dropped).
func (f *IAM) Publish(k *SigningKey) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published = append(f.published, k)
	if len(f.published) > 3 {
		f.published = f.published[len(f.published)-3:]
	}
}

// Current is the key tokens are signed with by default.
func (f *IAM) Current() *SigningKey { f.mu.Lock(); defer f.mu.Unlock(); return f.current }

// JWKSFetches counts GET /.well-known/jwks.json.
func (f *IAM) JWKSFetches() int64 { return f.fetches.Load() }

// Handler serves the discovery document and the JWKS.
func (f *IAM) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		f.fetches.Add(1)
		f.mu.Lock()
		keys := make([]map[string]string, 0, len(f.published))
		for _, k := range f.published {
			keys = append(keys, k.JWK())
		}
		f.mu.Unlock()
		w.Header().Set("Cache-Control", "public, max-age=3600")
		writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
	})
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		base := f.baseURL
		f.mu.Unlock()
		if base == "" {
			base = "http://" + r.Host
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"issuer":                                f.Issuer,
			"jwks_uri":                              base + "/.well-known/jwks.json",
			"token_endpoint":                        "/api/iam/token",
			"grant_types_supported":                 []string{"urn:ietf:params:oauth:grant-type:token-exchange", "refresh_token"},
			"token_endpoint_auth_methods_supported": []string{"none"},
			"id_token_signing_alg_values_supported": []string{"RS256", "ES256", "EdDSA"},
			"be_contract":                           "iam/1.0",
			"be_member":                             map[string]string{"id": "conformance/fake-iam", "version": "1.0.0"},
			"be_capabilities":                       []string{"core"},
			"be_tenant_id":                          f.Tenant,
			"be_access_token_ttl_seconds":           600,
		})
	})
	return mux
}

// TokenOpt changes how a token is minted.
type TokenOpt func(*tokenSpec)

type tokenSpec struct {
	header map[string]any
	claims map[string]any
	key    *SigningKey
	mode   string
	secret []byte
}

// WithClaim sets a claim (any JSON value).
func WithClaim(k string, v any) TokenOpt { return func(s *tokenSpec) { s.claims[k] = v } }

// WithoutClaim removes a claim.
func WithoutClaim(k string) TokenOpt { return func(s *tokenSpec) { delete(s.claims, k) } }

// WithHeader sets a header member.
func WithHeader(k string, v any) TokenOpt { return func(s *tokenSpec) { s.header[k] = v } }

// WithoutKid removes the header kid.
func WithoutKid() TokenOpt { return func(s *tokenSpec) { delete(s.header, "kid") } }

// AlgNone makes an unsigned token with alg none.
func AlgNone() TokenOpt {
	return func(s *tokenSpec) { s.header["alg"], s.mode = "none", "none" }
}

// HS256 signs with HMAC; the header keeps the current key's kid.
func HS256(secret []byte) TokenOpt {
	return func(s *tokenSpec) { s.header["alg"], s.mode, s.secret = "HS256", "hs256", secret }
}

// SignedBy signs with another key (its kid and alg go into the header).
func SignedBy(k *SigningKey) TokenOpt {
	return func(s *tokenSpec) { s.key, s.header["kid"], s.header["alg"] = k, k.Kid, k.Alg }
}

// Token mints a platform access token for p; with no options it is valid for 600 s.
func (f *IAM) Token(p Persona, opts ...TokenOpt) string {
	now := time.Now().Unix()
	key := f.Current()
	s := &tokenSpec{
		header: map[string]any{"alg": key.Alg, "kid": key.Kid, "typ": "JWT"},
		claims: map[string]any{
			"iss": f.Issuer, "aud": []string{f.Tenant}, "sub": p.Sub, "typ": "access",
			"iat": now, "nbf": now, "exp": now + 600, "jti": newID(),
			"tenant_id": f.Tenant, "azp": "pc",
		},
		key: key,
	}
	if len(p.Roles) > 0 {
		s.claims["roles"] = p.Roles
	}
	if p.DeptPath != nil && *p.DeptPath != "" {
		s.claims["dept_path"] = *p.DeptPath
	}
	if p.Act != nil {
		s.claims["act"] = p.Act
	}
	if p.Locale != "" {
		s.claims["locale"] = p.Locale
	}
	for _, o := range opts {
		o(s)
	}
	tok, err := encodeJWS(s.header, s.claims, s.key, s.mode, s.secret)
	if err != nil {
		panic(err) // keys are generated in process; signing cannot fail
	}
	return tok
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
