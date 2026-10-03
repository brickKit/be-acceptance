package fakes

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthzBundleMatchesContractAndHonoursETag(t *testing.T) {
	f := NewAuthz()
	f.SetRole("editor", []string{"a.b.view"}, RoleGrant{DefaultLevel: "all"})
	ts := httptest.NewServer(f.Handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/authz/v2/bundle")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err := validateContract("authz", "bundle.schema.json", b); err != nil {
		t.Fatalf("bundle: %v\n%s", err, b)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m["contract"] != "authz/2.0" {
		t.Errorf("contract = %v", m["contract"])
	}
	etag := resp.Header.Get("ETag")
	req, _ := http.NewRequest("GET", ts.URL+"/authz/v2/bundle", nil)
	req.Header.Set("If-None-Match", etag)
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusNotModified {
		t.Fatalf("If-None-Match = %d", r2.StatusCode)
	}
	f.SetStale("u1", 1759400000)
	r3, _ := http.DefaultClient.Do(req)
	r3.Body.Close()
	if r3.StatusCode != http.StatusOK || r3.Header.Get("ETag") == etag {
		t.Fatalf("changed bundle: %d %s", r3.StatusCode, r3.Header.Get("ETag"))
	}
	if f.BundleFetches() != 3 {
		t.Errorf("fetches = %d", f.BundleFetches())
	}
}

func TestAuthzChangefeedMatchesContract(t *testing.T) {
	f := NewAuthz()
	ts := httptest.NewServer(f.Handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/authz/v2/changes?types=a.b.c&after=0&limit=500")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if err := validateContract("authz", "changefeed.schema.json", b); err != nil {
		t.Fatalf("changes: %v\n%s", err, b)
	}
}
