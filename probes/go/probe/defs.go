package probe

import (
	"embed"
	"io/fs"
	"strings"

	"github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/config"
)

// The probe contract (be-acceptance probes/CONTRACT.md), the same in every language.
const (
	Version     = "3.2.15"
	WrittenType = "test.probe.written.v1"
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
	return []config.Key{
		{Name: "PROBE_NEXT", Type: config.String},          // the probe that call and relay go to; empty: none
		{Name: "PROBE_NEXT_URL", Type: config.String},      // its user surface ($endpoint:<id>), protocol P4.26
		{Name: "PROBE_NEXT_GRPC_URL", Type: config.String}, // its system surface ($endpoint:<id>:grpc)
		{Name: "PROBE_PEER_STREAM", Type: config.String},   // the peer's event stream to consume; empty: none
		{Name: "PROBE_GUARD", Type: config.Boolean, Default: false},
	}
}

// Prefix is a variant's resource type and the prefix of its operation keys: the component ID with "/" as "." and
// "-" as "_" (test/probe-go2 -> test.probe_go2), so no two components own the same keys.
func Prefix(id string) string {
	return strings.NewReplacer("/", ".", "-", "_").Replace(id)
}

// Authz declares a variant's one resource and its operations; they protect the user routes when PROBE_GUARD is on
// and the system methods always.
func Authz(id string) *authz.Definitions {
	p := Prefix(id)
	ops := []authz.Operation{{Key: p + ".read", ResourceType: p, Purpose: "read", RiskClass: "low"}}
	for _, op := range []string{"echo", "work", "write", "call"} {
		ops = append(ops, authz.Operation{Key: p + "." + op, ResourceType: p, Purpose: "action", RiskClass: "low"})
	}
	return &authz.Definitions{Component: id, Operations: ops, Resources: []authz.Resource{{Type: p,
		ViewOperation: p + ".read", Dimensions: map[string]authz.Dimension{}, Fields: map[string]authz.Field{}}}}
}
