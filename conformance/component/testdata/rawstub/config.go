package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// keySpec is one configSchema item of component.yaml, with the parse format the protocol
// catalogue (be-protocol schemas/config-keys.yaml) gives it.
type keySpec struct {
	name     string
	format   string // string, int, duration, url, family_url, enum, locale
	required bool
	def      *string
	secret   bool
	enum     []string
}

func str(s string) *string { return &s }

// declaredKeys mirrors component.yaml configSchema exactly; nothing else is read except
// COMPONENT_ID and COMPONENT_VERSION (P2.2).
var declaredKeys = []keySpec{
	{name: "PG_HOST", format: "string", required: true},
	{name: "PG_PORT", format: "int", def: str("5432")},
	{name: "PG_DATABASE", format: "string", required: true},
	{name: "PG_USER", format: "string", required: true},
	{name: "PG_PASSWORD_FILE", format: "string", required: true, secret: true},
	{name: "PG_OWNER_USER", format: "string", required: true},
	{name: "PG_OWNER_PASSWORD_FILE", format: "string", required: true, secret: true},
	{name: "PG_SCHEMA", format: "string", required: true},
	{name: "PG_POOL_MAX", format: "int", def: str("10")},
	{name: "PG_POOL_MIN_IDLE", format: "int", def: str("2")},
	{name: "PG_POOL_ACQUIRE_TIMEOUT", format: "duration", def: str("5s")},
	{name: "PG_CONN_MAX_LIFETIME", format: "duration", def: str("30m")},
	{name: "PG_CONN_MAX_IDLE_TIME", format: "duration", def: str("5m")},
	{name: "PG_MIGRATION_HOST", format: "string"},
	{name: "PG_MIGRATION_PORT", format: "int"},
	{name: "AUTHZ_URL", format: "family_url", required: true},
	{name: "IAM_URL", format: "family_url", required: true},
	{name: "IAM_ISSUER", format: "string", required: true},
	{name: "TENANT_ID", format: "string", required: true},
	{name: "OTEL_BASE_URL", format: "url", def: str("")},
	{name: "DEFAULT_LOCALE", format: "locale", def: str("zh-CN")},
	{name: "LOG_LEVEL", format: "enum", def: str("info"), enum: []string{"debug", "info", "warn", "error"}},
	{name: "HTTP_DEFAULT_TIMEOUT", format: "duration", def: str("10s")},
	{name: "GRPC_MAX_CONNECTION_AGE", format: "duration", def: str("5m")},
	{name: "SHUTDOWN_GRACE", format: "duration", def: str("25s")},
}

// Config is the parsed configuration of the process.
type Config struct {
	ComponentID, ComponentVersion string

	PGHost, PGDatabase, PGUser, PGPasswordFile string
	PGOwnerUser, PGOwnerPasswordFile, PGSchema string
	PGPort, PGPoolMax, PGPoolMinIdle           int
	PGAcquireTimeout, PGConnMaxLifetime        time.Duration
	PGConnMaxIdleTime                          time.Duration
	PGMigrationHost                            string
	PGMigrationPort                            int

	AuthzURL, IAMURL, IAMIssuer, TenantID string

	OtelBaseURL, DefaultLocale, LogLevel string
	HTTPDefaultTimeout, GRPCMaxConnAge   time.Duration
	ShutdownGrace                        time.Duration
}

// configProblem is one configuration error: the key and its class (CONFIG_MISSING,
// CONFIG_INVALID).
type configProblem struct {
	Key, Class, Detail string
}

// envLookup is how configuration is read; tests replace it.
type envLookup func(string) (string, bool)

// loadConfig parses every declared key and returns all problems together (P2.3).
// mode "serve" checks the runtime password file, "migrate" the owner password file
// (P10.12: the serving runtime never reads the owner's file).
func loadConfig(lookup envLookup, mode string) (*Config, []configProblem) {
	vals := map[string]parsed{}
	var probs []configProblem
	for _, k := range declaredKeys {
		raw, present := lookup(k.name)
		var rp *string
		if present {
			rp = &raw
		}
		v, err := parseValue(k, rp)
		if err != nil {
			probs = append(probs, configProblem{Key: k.name, Class: err.class, Detail: err.detail})
			continue
		}
		vals[k.name] = v
	}
	if id, ok := lookup("COMPONENT_ID"); ok && id != "" && id != componentID {
		probs = append(probs, configProblem{Key: "COMPONENT_ID", Class: "CONFIG_INVALID",
			Detail: fmt.Sprintf("injected component ID %q differs from the image's %q", id, componentID)})
	}
	secretKey := "PG_PASSWORD_FILE"
	if mode == "migrate" {
		secretKey = "PG_OWNER_PASSWORD_FILE"
	}
	if v, ok := vals[secretKey]; ok && v.set {
		if _, err := readSecretFile(v.s); err != nil {
			probs = append(probs, configProblem{Key: secretKey, Class: err.class, Detail: err.detail})
		}
	}
	if len(probs) > 0 {
		return nil, probs
	}
	return buildConfig(lookup, vals), nil
}

func buildConfig(lookup envLookup, v map[string]parsed) *Config {
	c := &Config{
		ComponentID: componentID, ComponentVersion: componentVersion,
		PGHost: v["PG_HOST"].s, PGDatabase: v["PG_DATABASE"].s, PGUser: v["PG_USER"].s,
		PGPasswordFile: v["PG_PASSWORD_FILE"].s, PGOwnerUser: v["PG_OWNER_USER"].s,
		PGOwnerPasswordFile: v["PG_OWNER_PASSWORD_FILE"].s, PGSchema: v["PG_SCHEMA"].s,
		PGPort: int(v["PG_PORT"].i), PGPoolMax: int(v["PG_POOL_MAX"].i), PGPoolMinIdle: int(v["PG_POOL_MIN_IDLE"].i),
		PGAcquireTimeout: v["PG_POOL_ACQUIRE_TIMEOUT"].d, PGConnMaxLifetime: v["PG_CONN_MAX_LIFETIME"].d,
		PGConnMaxIdleTime: v["PG_CONN_MAX_IDLE_TIME"].d,
		AuthzURL:          v["AUTHZ_URL"].s, IAMURL: v["IAM_URL"].s, IAMIssuer: v["IAM_ISSUER"].s, TenantID: v["TENANT_ID"].s,
		OtelBaseURL: strings.TrimSuffix(v["OTEL_BASE_URL"].s, "/"), DefaultLocale: v["DEFAULT_LOCALE"].s,
		LogLevel: v["LOG_LEVEL"].s, HTTPDefaultTimeout: v["HTTP_DEFAULT_TIMEOUT"].d,
		GRPCMaxConnAge: v["GRPC_MAX_CONNECTION_AGE"].d, ShutdownGrace: v["SHUTDOWN_GRACE"].d,
		PGMigrationHost: v["PG_MIGRATION_HOST"].s, PGMigrationPort: int(v["PG_MIGRATION_PORT"].i),
	}
	if !v["PG_MIGRATION_HOST"].set {
		c.PGMigrationHost = c.PGHost
	}
	if !v["PG_MIGRATION_PORT"].set {
		c.PGMigrationPort = c.PGPort
	}
	if ver, ok := lookup("COMPONENT_VERSION"); ok && ver != "" {
		c.ComponentVersion = ver
	}
	return c
}

// readSecretFile reads a text secret: exactly one trailing LF or CRLF is removed; an empty
// result is CONFIG_MISSING (P2.9, vectors config secret_text).
func readSecretFile(path string) (string, *parseErr) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", &parseErr{"CONFIG_INVALID", "secret file cannot be read"}
	}
	v, ok := secretText(string(b))
	if !ok {
		return "", &parseErr{"CONFIG_MISSING", "secret file is empty"}
	}
	return v, nil
}

func secretText(content string) (string, bool) {
	if strings.HasSuffix(content, "\r\n") {
		content = content[:len(content)-2]
	} else if strings.HasSuffix(content, "\n") {
		content = content[:len(content)-1]
	}
	return content, content != ""
}
