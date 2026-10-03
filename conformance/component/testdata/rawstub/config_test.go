package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func baseEnv(t *testing.T) map[string]string {
	dir := t.TempDir()
	pw := filepath.Join(dir, "PG_PASSWORD_FILE")
	_ = os.WriteFile(pw, []byte("s3cr3t\n"), 0o600)
	return map[string]string{
		"PG_HOST": "pg", "PG_DATABASE": "db", "PG_USER": "rw", "PG_PASSWORD_FILE": pw,
		"PG_OWNER_USER": "own", "PG_OWNER_PASSWORD_FILE": filepath.Join(dir, "missing"), "PG_SCHEMA": "s",
		"AUTHZ_URL": "http://authz:8223/", "IAM_URL": "http://iam:8200", "IAM_ISSUER": "urn:be:t1:iam", "TENANT_ID": "t1",
		"COMPONENT_ID": componentID, "COMPONENT_VERSION": "1.0.0",
	}
}

func lookupFrom(m map[string]string) envLookup {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, probs := loadConfig(lookupFrom(baseEnv(t)), "serve")
	if probs != nil {
		t.Fatalf("problems: %v", probs)
	}
	if cfg.PGPort != 5432 || cfg.PGPoolMax != 10 || cfg.ShutdownGrace != 25*time.Second || cfg.DefaultLocale != "zh-CN" ||
		cfg.AuthzURL != "http://authz:8223" || cfg.PGMigrationHost != "pg" || cfg.PGMigrationPort != 5432 || cfg.OtelBaseURL != "" {
		t.Errorf("defaults wrong: %+v", cfg)
	}
}

func TestLoadConfigReportsAllProblems(t *testing.T) {
	env := baseEnv(t)
	delete(env, "PG_HOST")
	env["PG_PORT"] = "+5"
	env["SHUTDOWN_GRACE"] = "25"
	env["AUTHZ_URL"] = "http://authz"
	env["LOG_LEVEL"] = "WARN"
	env["COMPONENT_ID"] = "conformance/other"
	_, probs := loadConfig(lookupFrom(env), "serve")
	var keys []string
	for _, p := range probs {
		keys = append(keys, p.Key+"="+p.Class)
	}
	sort.Strings(keys)
	want := "AUTHZ_URL=CONFIG_INVALID,COMPONENT_ID=CONFIG_INVALID,LOG_LEVEL=CONFIG_INVALID,PG_HOST=CONFIG_MISSING,PG_PORT=CONFIG_INVALID,SHUTDOWN_GRACE=CONFIG_INVALID"
	if strings.Join(keys, ",") != want {
		t.Errorf("got %s", strings.Join(keys, ","))
	}
}

func TestLoadConfigSecrets(t *testing.T) {
	env := baseEnv(t)
	if _, probs := loadConfig(lookupFrom(env), "migrate"); len(probs) != 1 || probs[0].Key != "PG_OWNER_PASSWORD_FILE" {
		t.Errorf("migrate must read the owner's file: %v", probs)
	}
	empty := filepath.Join(t.TempDir(), "empty")
	_ = os.WriteFile(empty, []byte("\n"), 0o600)
	env["PG_PASSWORD_FILE"] = empty
	if _, probs := loadConfig(lookupFrom(env), "serve"); len(probs) != 1 || probs[0].Class != "CONFIG_MISSING" {
		t.Errorf("an empty secret file is CONFIG_MISSING: %v", probs)
	}
	env["PG_PASSWORD_FILE"] = "s3cr3t"
	if _, probs := loadConfig(lookupFrom(env), "serve"); len(probs) != 1 || probs[0].Class != "CONFIG_INVALID" {
		t.Errorf("a secret value in the environment is CONFIG_INVALID: %v", probs)
	}
}

func TestLogLineTruncationAndRedaction(t *testing.T) {
	var buf bytes.Buffer
	l := newLogger("info", componentID, "1.0.0")
	l.out = &buf
	l.Debug("hidden", nil)
	l.Info("owner_profile_updated", F{"phone": "+86 138", "email": "a@x.cn", "display_name": "A", "note": strings.Repeat("é", 3000), "trace_id": "t"})
	line := strings.TrimSpace(buf.String())
	if strings.Count(buf.String(), "\n") != 1 || len(line) > maxLine {
		t.Fatalf("want one line of at most 2 KiB, got %d bytes", len(line))
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if rec["phone"] != "[REDACTED]" || rec["email"] != "[REDACTED]" || rec["display_name"] != "A" || rec["truncated"] != true ||
		!strings.HasSuffix(rec["note"].(string), "…[TRUNCATED]") || rec["msg"] != "owner_profile_updated" {
		t.Errorf("record: %v", rec)
	}
}

func TestUUIDv7(t *testing.T) {
	at := time.UnixMilli(1_800_000_000_123)
	id := newUUIDv7(at)
	if !uuidRe.MatchString(id) || id[14] != '7' || !strings.ContainsAny(id[19:20], "89ab") || id != strings.ToLower(id) {
		t.Errorf("not a v7 UUID: %s", id)
	}
	if want := fmt.Sprintf("%012x", at.UnixMilli()); id[:8]+id[9:13] != want {
		t.Errorf("timestamp %s, want %s", id[:8]+id[9:13], want)
	}
}
