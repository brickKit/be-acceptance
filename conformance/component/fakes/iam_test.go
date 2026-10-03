package fakes

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

func newIAMForTest(t *testing.T) (*IAM, *httptest.Server) {
	t.Helper()
	f, err := NewIAM("urn:be:compconf:iam", "compconf")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(f.Handler())
	t.Cleanup(ts.Close)
	return f, ts
}

func fetchJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func keyfunc(t *testing.T, ts *httptest.Server) jwt.Keyfunc {
	return func(tok *jwt.Token) (any, error) {
		resp, err := http.Get(ts.URL + "/.well-known/jwks.json")
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return PublicKeyFromJWKS(b, tok.Header["kid"].(string))
	}
}

func TestIAMValidTokenVerifiesWithIndependentLibrary(t *testing.T) {
	f, ts := newIAMForTest(t)
	dept := "/1/3/"
	raw := f.Token(Persona{Sub: "0192aaaa-0000-7000-8000-000000000001", Roles: []string{"r1"}, DeptPath: &dept})
	claims := jwt.MapClaims{}
	tok, err := jwt.ParseWithClaims(raw, claims, keyfunc(t, ts), jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithIssuer("urn:be:compconf:iam"), jwt.WithAudience("compconf"))
	if err != nil || !tok.Valid {
		t.Fatalf("token does not verify: %v", err)
	}
	for _, c := range []string{"iss", "aud", "sub", "typ", "iat", "exp", "jti", "nbf", "tenant_id", "azp"} {
		if _, ok := claims[c]; !ok {
			t.Errorf("claim %s missing", c)
		}
	}
	if claims["typ"] != "access" || claims["dept_path"] != "/1/3/" {
		t.Errorf("claims = %v", claims)
	}
	if f.JWKSFetches() == 0 {
		t.Error("JWKS fetches not counted")
	}
}

func TestIAMTokenVariants(t *testing.T) {
	f, _ := newIAMForTest(t)
	p := Persona{Sub: "s1"}
	hdr := func(raw string) map[string]any {
		parts := strings.Split(raw, ".")
		var h map[string]any
		_ = json.Unmarshal(mustB64(t, parts[0]), &h)
		return h
	}
	payload := func(raw string) map[string]any {
		parts := strings.Split(raw, ".")
		var c map[string]any
		_ = json.Unmarshal(mustB64(t, parts[1]), &c)
		return c
	}
	none := f.Token(p, AlgNone())
	if hdr(none)["alg"] != "none" || !strings.HasSuffix(none, ".") {
		t.Errorf("alg none token = %s", none)
	}
	if hdr(f.Token(p, HS256([]byte("k"))))["alg"] != "HS256" {
		t.Error("HS256 header")
	}
	if _, ok := hdr(f.Token(p, WithoutKid()))["kid"]; ok {
		t.Error("kid present")
	}
	if payload(f.Token(p, WithClaim("typ", "refresh")))["typ"] != "refresh" {
		t.Error("typ override")
	}
	if _, ok := payload(f.Token(p, WithoutClaim("exp")))["exp"]; ok {
		t.Error("exp present")
	}
	if _, ok := payload(f.Token(Persona{Sub: "x", DeptPath: ptr("")}))["dept_path"]; ok {
		t.Error("empty dept_path must be omitted (TOKENS.md)")
	}
}

func TestIAMRotationPublishesNewKey(t *testing.T) {
	f, ts := newIAMForTest(t)
	k2, err := f.NewKey("ES256")
	if err != nil {
		t.Fatal(err)
	}
	raw := f.Token(Persona{Sub: "s"}, SignedBy(k2))
	if _, err := jwt.Parse(raw, keyfunc(t, ts)); err == nil {
		t.Fatal("unpublished key verified")
	}
	f.Publish(k2)
	if _, err := jwt.Parse(raw, keyfunc(t, ts), jwt.WithValidMethods([]string{"ES256"})); err != nil {
		t.Fatalf("published key does not verify: %v", err)
	}
	keys := fetchJSON(t, ts.URL+"/.well-known/jwks.json")["keys"].([]any)
	if len(keys) != 2 {
		t.Fatalf("jwks keys = %d", len(keys))
	}
}

func TestIAMDocumentsMatchContractSchemas(t *testing.T) {
	_, ts := newIAMForTest(t)
	for path, schema := range map[string]string{
		"/.well-known/jwks.json":                  "jwks.schema.json",
		"/.well-known/oauth-authorization-server": "server-metadata.schema.json",
	} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err := validateContract("iam", schema, b); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
}

func ptr(s string) *string { return &s }
