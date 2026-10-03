package compconf

import (
	"strings"
	"testing"
)

func TestComponentEnvWidget(t *testing.T) {
	c := widget(t)
	v := suiteValues{FakeHost: "host.docker.internal", AuthzPort: 1001, IAMPort: 1002, ObserverPort: 1003,
		PeerHTTP: map[string]int{"conformance/peer": 1004}, PeerGRPC: map[string]int{"conformance/peer": 1005},
		DB:     dbIdentity{Database: "compconf", Owner: "o1", OwnerPassword: "opw", Runtime: "r1", RuntimePassword: "rpw", Schema: "s1"},
		Issuer: "urn:be:compconf:iam", Tenant: "compconf", Extra: map[string]string{"S3_URL": "http://s3", "S3_BUCKET": "b",
			"S3_ACCESS_KEY_ID_FILE": "ak", "S3_SECRET_ACCESS_KEY_FILE": "sk"}}
	env, secrets, err := componentEnv(c, v)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, kv := range env {
		k, val, _ := strings.Cut(kv, "=")
		m[k] = val
	}
	want := map[string]string{
		"COMPONENT_ID": "conformance/widget", "COMPONENT_VERSION": "1.0.0",
		"PG_HOST": "pg", "PG_PORT": "5432", "PG_USER": "r1", "PG_OWNER_USER": "o1", "PG_SCHEMA": "s1",
		"PG_PASSWORD_FILE":               "/run/brickkit/secrets/conformance-widget-1-0-0/PG_PASSWORD_FILE",
		"AUTHZ_URL":                      "http://host.docker.internal:1001",
		"IAM_URL":                        "http://host.docker.internal:1002",
		"OTEL_BASE_URL":                  "http://host.docker.internal:1003",
		"CONFORMANCE_PEER_ENDPOINT":      "http://host.docker.internal:1004",
		"CONFORMANCE_PEER_GRPC_ENDPOINT": "http://host.docker.internal:1005",
		"NATS_URL":                       "nats://nats:4222",
		"S3_FORCE_PATH_STYLE":            "false",
		"WIDGET_RESERVE_HOLD_SECONDS":    "900",
		"EVENTS_MAX_DELIVER":             "3",
	}
	for k, w := range want {
		if m[k] != w {
			t.Errorf("%s = %q, want %q", k, m[k], w)
		}
	}
	if _, ok := m["MDM_ORG_ENDPOINT"]; ok {
		t.Error("an optional dependency the suite does not install must have no *_ENDPOINT (P2.5)")
	}
	if _, ok := m["PG_MIGRATION_HOST"]; ok {
		t.Error("a key without default and without suite value must be absent")
	}
	if secrets["PG_PASSWORD_FILE"] != "rpw" || secrets["PG_OWNER_PASSWORD_FILE"] != "opw" || secrets["S3_SECRET_ACCESS_KEY_FILE"] != "sk" {
		t.Errorf("secrets = %v", secrets)
	}
	for _, kv := range env {
		if strings.Contains(kv, "rpw") || strings.Contains(kv, "opw") {
			t.Errorf("secret value in env: %s", kv)
		}
	}
}

func TestComponentEnvRequiredKeyWithoutValue(t *testing.T) {
	c := rawstub(t)
	_, _, err := componentEnv(c, suiteValues{})
	if err == nil || !strings.Contains(err.Error(), "PG_HOST") {
		t.Fatalf("err = %v", err)
	}
}
