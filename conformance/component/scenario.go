package compconf

import "context"

// ImplementedProfiles are the profiles this suite version runs.
var ImplementedProfiles = []string{"core", "obs", "err", "auth", "grpc"}

// step is one ordered action of the scenario, attributed to the case it exercises; Case ""
// marks a setup step several cases depend on, which always runs.
type step struct {
	Case string
	Name string
	Fn   func(ctx context.Context, r *Run)
}

// scenario is the order of the run. Several cases are observed in more than one step
// (CP-CORE-04 and -05 before and after readiness); a case passes when every observation does.
func scenario() []step {
	return []step{
		{"CP-CORE-12", "component.yaml declarations", caseCore12},
		{"CP-CORE-14", "secret declarations", caseCore14Static},
		{"CP-CORE-01", "migrate twice; unknown argument", caseCore01},
		{"CP-CORE-02", "configuration errors exit 78", caseCore02},
		{"", "start with PostgreSQL, the bus and authz down", stepStartWithDepsDown},
		{"CP-CORE-04", "/healthz with PostgreSQL stopped", caseCore04},
		{"", "PostgreSQL back", stepDatabaseBack},
		{"CP-AUTH-10", "protected routes before the first bundle", caseAuth10},
		{"", "authz and the bus back", stepProvidersBack},
		{"CP-CORE-03", "recovered after dependencies came back", caseCore03},
		{"CP-CORE-05", "readiness latch", caseCore05Latch},
		{"CP-AUTH-13", "decisions with authz stopped", caseAuth13},
		{"", "authz back", stepAuthzBack},
		{"CP-CORE-07", "/bin/sh and wget", caseCore07},
		{"CP-CORE-10", "request ID", caseCore10},
		{"CP-CORE-11", "/_be/info", caseCore11},
		{"CP-CORE-13", "IPv4 and IPv6", caseCore13},
		{"CP-CORE-14", "no secret in the environment", caseCore14Runtime},
		{"CP-CORE-09", "body limit", caseCore09},
		{"CP-CORE-08", "slow request headers", caseCore08},
		{"CP-AUTH-01", "no or malformed token", caseAuth01},
		{"CP-AUTH-02", "algorithms and kid", caseAuth02},
		{"CP-AUTH-03", "refresh token and claim types", caseAuth03},
		{"CP-AUTH-04", "issuer", caseAuth04},
		{"CP-AUTH-05", "audience", caseAuth05},
		{"CP-AUTH-06", "expiry", caseAuth06},
		{"CP-AUTH-07", "JWKS rotation", caseAuth07},
		{"CP-AUTH-09", "every operation guarded", caseAuth09},
		{"CP-AUTH-11", "missing permission", caseAuth11},
		{"CP-AUTH-12", "unsupported delegation", caseAuth12},
		{"CP-OBS-04", "personal data redacted", caseObs04PII},
		{"CP-OBS-01", "trace parent", caseObs01},
		{"CP-ERR-02", "gRPC errors", caseErr02},
		{"CP-RPC-01", "no be-caller", caseRPC01},
		{"CP-RPC-02", "caller in the gRPC log", caseRPC02},
		{"CP-RPC-03", "user-facing rpc over gRPC", caseRPC03},
		{"CP-RPC-04", "receive limit", caseRPC04},
		{"CP-RPC-06", "batch limit", caseRPC06},
		{"CP-RPC-05", "GOAWAY after MaxConnectionAge", caseRPC05},
		{"CP-ERR-03", "revoked grant answers generic INTERNAL", caseErr03},
		{"CP-AUTH-08", "stale token", caseAuth08},
		{"CP-OBS-03", "metrics", caseObs03},
		{"CP-CORE-06", "SIGTERM", caseCore06},
		{"CP-OBS-02", "log lines", caseObs02},
		{"CP-OBS-04", "no secret or token in logs", caseObs04Leaks},
		{"CP-OBS-05", "LOG_LEVEL=warn", caseObs05},
		{"CP-ERR-01", "every error is a catalogued problem", caseErr01},
		{"CP-ERR-04", "every reason catalogued", caseErr04},
	}
}
