package probe

import (
	"embed"
	"io/fs"
	"strings"
	"time"

	"github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/config"
)

// The probe contract (be-acceptance probes/CONTRACT.md), the same in every language.
const (
	Version     = "3.2.54"
	WrittenType = "test.probe.written.v1"
	NotedType   = "test.probe.noted.v1" // since 3.2.23: a write asked as "noted" (suite D, V3)
	MaxLabel    = 64
	RelayBytes  = 1024
	MaxWorkMS   = 1000
	MaxBytes    = 512 << 10
)

// Variant is one probe component: the same code under its own component ID, ports and event stream, so a shell can
// host several and two probes can call each other inside one process.
type Variant struct {
	ID       string
	Stream   string
	HTTPPort int // the port its image listens on alone (deployment.port)
	GRPCPort int
}

// Variants are the Go probe components (registry/ports.tsv of the assembly project).
var Variants = []Variant{
	{"test/probe-go", "test.probe_go", 8600, 9600},
	{"test/probe-go2", "test.probe_go2", 8602, 9602},
	{"test/probe-go3", "test.probe_go3", 8603, 9603},
	{"test/probe-go4", "test.probe_go4", 8604, 9604},
}

// Suite is the conformance suite's own service identity: it pulls the watermark stream (since 3.2.31).
const Suite = "test/acceptance"

// WatermarkStream is a variant's watermark source stream (since 3.2.31, Go): the suite's watermark profile pulls it.
func WatermarkStream(v Variant) string { return v.Stream + "_wm" }

// Callers are the components allowed to call the probes' system methods: every probe of either language.
var Callers = []string{"test/probe-go", "test/probe-go2", "test/probe-go3", "test/probe-go4", "test/probe-py",
	"test/probe-py2"}

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations are the probe's migration files.
func Migrations() fs.FS {
	sub, _ := fs.Sub(migrationFiles, "migrations")
	return sub
}

// Keys are the probe's own configuration keys.
func Keys() []config.Key {
	return append([]config.Key{
		{Name: "PROBE_NEXT", Type: config.String},          // the probe that call and relay go to; empty: none
		{Name: "PROBE_NEXT_URL", Type: config.String},      // its user surface ($endpoint:<id>), protocol P4.26
		{Name: "PROBE_NEXT_GRPC_URL", Type: config.String}, // its system surface ($endpoint:<id>:grpc)
		{Name: "PROBE_PEER_STREAM", Type: config.String},   // the peer's event stream to consume; empty: none
		{Name: "PROBE_GUARD", Type: config.Boolean, Default: false},
		// since 3.2.30: how often the steps singleton runs (the background work profile); 0: no such job
		{Name: "PROBE_STEPS_EVERY", Type: config.Duration, Default: time.Duration(0)},
		// since 3.2.32: the cron job of the background work profile (a slot a minute); off by default
		{Name: "PROBE_CRON", Type: config.Boolean, Default: false},
		// since 3.2.38: a guarded route naming an operation the probe does not declare, so the start fails (CK-AZ-01)
		{Name: "PROBE_UNDECLARED_ROUTE", Type: config.Boolean, Default: false},
	}, ExternalKeys()...) // the payment (since 3.2.41)
}

// Prefix is a variant's resource type and the prefix of its operation keys: the component ID with "/" as "." and
// "-" as "_" (test/probe-go2 -> test.probe_go2), so no two components own the same keys.
func Prefix(id string) string {
	return strings.NewReplacer("/", ".", "-", "_").Replace(id)
}

// Authz declares a variant's resources and their operations: the probe's own (it protects the user routes when
// PROBE_GUARD is on and the system methods always) and, since 3.2.38, the entry of the authorization profile.
func Authz(id string) *authz.Definitions {
	p := Prefix(id)
	ops := []authz.Operation{{Key: p + ".read", ResourceType: p, Purpose: "read", RiskClass: "low"},
		{Key: p + ".resend", ResourceType: p, Purpose: "read", RiskClass: "low"},  // the redelivery (P6.38, since 3.2.39)
		{Key: p + ".recover", ResourceType: p, Purpose: "read", RiskClass: "low"}} // a full recovery's checks (chapter 14, since 3.2.42)
	for _, op := range []string{"echo", "work", "write", "call", "note", "pull", OpPay} { // pull since 3.2.31, pay since 3.2.41
		ops = append(ops, authz.Operation{Key: p + "." + op, ResourceType: p, Purpose: "action", RiskClass: "low"})
	}
	ops = append(ops, entryOperations(p)...) // since 3.2.38: the authorization profile's entries
	return &authz.Definitions{Component: id, Operations: ops, Resources: []authz.Resource{{Type: p,
		ViewOperation: p + ".read", Dimensions: map[string]authz.Dimension{}, Fields: map[string]authz.Field{}},
		entryResource(p)}}
}
