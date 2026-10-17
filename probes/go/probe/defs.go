package probe

import (
	"embed"
	"io/fs"

	"github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/config"
	"github.com/brickKit/be-sdk-go/v3/events"
)

// The probe contract (be-acceptance probes/CONTRACT.md), the same in every language.
const (
	ID          = "test/probe-go"
	Version     = "3.0.0"
	GRPCPort    = 9600
	Stream      = "test.probe_go"
	WrittenType = "test.probe.written.v1"
	RelayBytes  = 1024
	MaxWorkMS   = 1000
	MaxBytes    = 512 << 10
)

// Callers are the components allowed to call the probe's system methods.
var Callers = []string{"test/probe-go", "test/probe-py"}

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
		{Name: "PROBE_NEXT", Type: config.String},        // the probe that call and relay go to; empty: none
		{Name: "PROBE_PEER_STREAM", Type: config.String}, // the peer's event stream to consume; empty: none
		{Name: "PROBE_GUARD", Type: config.Boolean, Default: false},
	}
}

// Streams the probe produces.
func Streams() []events.Stream { return []events.Stream{{Name: Stream, Partitions: 4}} }

// Authz declares the probe's one resource and its operations; they protect the user routes when PROBE_GUARD is on
// and the system methods always.
func Authz() *authz.Definitions {
	ops := []authz.Operation{{Key: "test.probe.read", ResourceType: "test.probe", Purpose: "read", RiskClass: "low"}}
	for _, op := range []string{"echo", "work", "write", "call"} {
		ops = append(ops, authz.Operation{Key: "test.probe." + op, ResourceType: "test.probe", Purpose: "action", RiskClass: "low"})
	}
	return &authz.Definitions{Component: ID, Operations: ops, Resources: []authz.Resource{{Type: "test.probe",
		ViewOperation: "test.probe.read", Dimensions: map[string]authz.Dimension{}, Fields: map[string]authz.Field{}}}}
}
