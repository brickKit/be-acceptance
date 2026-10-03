package compconf

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/brickKit/be-acceptance/conformance/component/fakes"
	"github.com/brickKit/be-acceptance/conformance/component/protoschema"
	beprotocol "github.com/brickKit/be-protocol"
	"google.golang.org/protobuf/types/dynamicpb"
	"gopkg.in/yaml.v3"
)

// The lifecycle profile (P16).

// stepLife01Migrated (CP-LIFE-01, before the component serves): right after the migration
// every partitioned table has a partition holding now, named from its lower bound (P16.6,
// P16.10).
func stepLife01Migrated(ctx context.Context, r *Run) {
	const id = "CP-LIFE-01"
	if r.pg == nil || !contains(r.ran, "lifecycle") {
		return
	}
	conn, err := r.dbConn(ctx)
	if !r.ev.require(id, err == nil, "connecting: %v", err) {
		return
	}
	defer conn.Close(ctx)
	parents, err := queryStrings(ctx, conn, `SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	    WHERE n.nspname = $1 AND c.relkind = 'p' AND NOT c.relispartition`, r.db.Schema)
	if !r.ev.require(id, err == nil, "reading the catalogue: %v", err) {
		return
	}
	now := time.Now()
	for _, p := range parents {
		parts, err := partitions(ctx, conn, p)
		if err != nil {
			r.ev.fail(id, "%s: %v", p, err)
			continue
		}
		holds := false
		for _, pt := range parts {
			holds = holds || (!pt.from.After(now) && pt.to.After(now))
			if want := partitionName(p, pt); want != "" && pt.name != want {
				r.ev.fail(id, "partition %s of %s [%s, %s) should be named %s (P16.10)", pt.name, p, pt.from.UTC().Format(time.RFC3339), pt.to.UTC().Format(time.RFC3339), want)
			}
		}
		r.ev.check(id, holds, "right after the migration %s has no partition holding now (P16.6: a database migrated on any day accepts writes that day)", p)
	}
}

// partitionName is P16.10's name for a range partition of a week, month or year ("" for
// another width).
func partitionName(parent string, p partition) string {
	f, t := p.from.UTC(), p.to.UTC()
	switch {
	case t.Sub(f) == 7*24*time.Hour && f.Weekday() == time.Monday:
		y, w := f.ISOWeek()
		return fmt.Sprintf("%s_%dw%02d", parent, y, w)
	case f.Day() == 1 && t.Equal(f.AddDate(0, 1, 0)):
		return fmt.Sprintf("%s_%dm%02d", parent, f.Year(), int(f.Month()))
	case f.YearDay() == 1 && t.Equal(f.AddDate(1, 0, 0)):
		return fmt.Sprintf("%s_%d", parent, f.Year())
	}
	return ""
}

// CP-LIFE-01 (write): the first business write after start succeeds.
func caseLife01Write(ctx context.Context, r *Run) {
	const id = "CP-LIFE-01"
	if !r.needDB(id) {
		return
	}
	res, _, err := r.createAny(ctx)
	r.ev.check(id, err == nil, "the first write after migration (%s create): %v (P16.6)", res, err)
}

// CP-LIFE-02: a list asked for a range is complete or answers 400 RANGE_COLD with its metadata
// (P16.3). Nothing can be frozen during a run (the 1.0 default cold store is none), so the case
// checks that an old range is answered completely, never truncated or refused otherwise.
func caseLife02(ctx context.Context, r *Run) {
	const id = "CP-LIFE-02"
	var list FixtureOp
	found := false
	for _, res := range sortedKeys(r.comp.Fixtures.Resources) {
		if l, ok := r.comp.Fixtures.Resources[res]["list"]; ok && l.Range != nil {
			list, found = l, true
			break
		}
	}
	if !found {
		r.ev.notApplicable(id, "no fixtures list declares a range")
		return
	}
	if !r.needMain(id) {
		return
	}
	x := r.invoke(ctx, list, "", "", map[string]string{list.Range.After: "2001-01-01T00:00:00Z", list.Range.Before: "2001-02-01T00:00:00Z"})
	switch {
	case x.Status == 200:
		r.ev.pass(id, "an old range answered completely (nothing is cold in a run)")
	case x.Status == 400 && x.reason() == "RANGE_COLD":
		for _, k := range []string{"online_from", "cold_ranges", "thaw_allowed", "export_allowed"} {
			r.ev.check(id, x.metadata(k) != "", "RANGE_COLD without metadata.%s (P16.3)", k)
		}
	default:
		r.ev.fail(id, "a list of 2001-01 = %d %s, want the complete result or 400 RANGE_COLD (P16.3)", x.Status, x.reason())
	}
}

// lifecycleOps are the operations of be-protocol openapi/resource-lifecycle.yaml with
// {domain}/{name} and the key templates filled for the component.
func (r *Run) lifecycleOps() ([]Operation, error) {
	ops, err := parseOpenAPI(beprotocol.FS, "openapi/resource-lifecycle.yaml")
	if err != nil {
		return nil, err
	}
	stem := strings.ReplaceAll(r.comp.ID(), "/", ".")
	for i := range ops {
		ops[i].Path = strings.Replace(ops[i].Path, "/{domain}/{name}", "/"+r.comp.ID(), 1)
		ops[i].Guard = strings.Replace(ops[i].Guard, "<domain>.<name>", stem, 1)
	}
	return ops, nil
}

// CP-LIFE-03: every _lifecycle operation is mounted and guarded by its key (401 without a
// token, 403 MISSING_PERMISSION without the key); one not implemented answers 501
// CAPABILITY_UNAVAILABLE with metadata.capability; the same holds for be.lifecycle.v1.Lifecycle
// on the grpc port (P16.4, P16.8).
func caseLife03(ctx context.Context, r *Run) {
	const id = "CP-LIFE-03"
	if !r.needMain(id) {
		return
	}
	ops, err := r.lifecycleOps()
	if !r.ev.require(id, err == nil && len(ops) > 0, "reading resource-lifecycle.yaml: %v", err) {
		return
	}
	for _, op := range ops {
		var body []reqOpt
		if op.Method != "GET" && op.Method != "DELETE" {
			body = append(body, withBody([]byte(`{"days":1,"table":"compconf","reason":"compconf"}`)))
		}
		t := r.target(op)
		x := r.call(ctx, op.Method, t, body...)
		r.ev.check(id, x.Status == 401 && x.reason() == "TOKEN_INVALID", "%s %s without a token = %d %s, want 401: not mounted or not guarded (P16.4)", op.Method, op.Path, x.Status, x.reason())
		x = r.call(ctx, op.Method, t, append(body, withToken(r.token(pNone)))...)
		r.ev.check(id, x.Status == 403 && x.metadata("permission") == op.Guard,
			"%s %s without %s = %d %s permission=%q, want 403 MISSING_PERMISSION (P16.8)", op.Method, op.Path, op.Guard, x.Status, x.reason(), x.metadata("permission"))
		x = r.call(ctx, op.Method, t, append(body, withToken(r.token(pAll)))...)
		if x.Status == 501 {
			r.ev.check(id, x.reason() == "CAPABILITY_UNAVAILABLE" && x.metadata("capability") != "",
				"%s %s = 501 %s capability=%q, want CAPABILITY_UNAVAILABLE with metadata.capability (P16.4)", op.Method, op.Path, x.reason(), x.metadata("capability"))
		} else {
			r.ev.check(id, x.Err == nil && x.Status != 404 && x.Status != 405 && x.Status < 500,
				"%s %s with every key = %d %s (P16.4)", op.Method, op.Path, x.Status, x.reason())
		}
	}
	r.lifecycleGRPC(ctx, id)
}

func (r *Run) lifecycleGRPC(ctx context.Context, id string) {
	if r.comp.GRPCPort() == 0 {
		return
	}
	root, _ := fs.Sub(beprotocol.FS, "proto") // its only service is be.lifecycle.v1.Lifecycle
	methods, err := fakes.CompileProtos(root, fakes.ProtoImports(root)...)
	if !r.ev.require(id, err == nil, "compiling be/lifecycle/v1: %v", err) {
		return
	}
	cc, ok := r.grpcConn(id)
	if !ok {
		return
	}
	defer cc.Close()
	for _, name := range sortedKeys(methods) {
		md := methods[name]
		st := invokeRaw(ctx, cc, name, md, dynamicpb.NewMessage(md.Input()), "compconf/suite", "compconf-life03")
		ei := errorInfo(st)
		r.recordGRPC(ei)
		mounted := st.Code() != 12 || (ei != nil && ei.Reason == "CAPABILITY_UNAVAILABLE" && ei.Metadata["capability"] != "")
		r.ev.check(id, mounted, "%s = %s %v: be.lifecycle.v1.Lifecycle is not mounted (UNIMPLEMENTED without CAPABILITY_UNAVAILABLE) (P16.4)", name, st.Code(), ei)
	}
}

// official migration-state tables, exempt from lifecycle.yaml (P11.11).
func officialStateTable(t, schema string) bool {
	switch t {
	case "schema_migrations_" + schema, "besdk_migrations_" + schema, "pgmigrations_" + schema,
		"_yoyo_migration", "_yoyo_log", "_yoyo_version", "yoyo_lock":
		return true
	}
	return strings.HasPrefix(t, "besdk_")
}

// CP-LIFE-04: migrations/lifecycle.yaml passes lifecycle.schema.json and declares every table
// of the migrated schema but the besdk_* and official migration-state tables (P16.1, P11.11).
func caseLife04(ctx context.Context, r *Run) {
	const id = "CP-LIFE-04"
	b, err := fs.ReadFile(r.comp.FS, "migrations/lifecycle.yaml")
	if !r.ev.check(id, err == nil, "migrations/lifecycle.yaml: %v (P16.1)", err) {
		return
	}
	s, err := protoschema.ProtocolSchema("lifecycle.schema.json")
	if r.ev.check(id, err == nil, "loading lifecycle.schema.json: %v", err) {
		r.ev.check(id, protoschema.ValidateYAML(s, b) == nil, "lifecycle.yaml does not match lifecycle.schema.json: %v", protoschema.ValidateYAML(s, b))
	}
	var doc struct {
		Tables map[string]yaml.Node `yaml:"tables"`
	}
	_ = yaml.Unmarshal(b, &doc)
	if r.pg == nil {
		return
	}
	conn, err := r.dbConn(ctx)
	if !r.ev.require(id, err == nil, "connecting: %v", err) {
		return
	}
	defer conn.Close(ctx)
	tables, err := queryStrings(ctx, conn, `SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	    WHERE n.nspname = $1 AND c.relkind IN ('r','p') AND NOT c.relispartition`, r.db.Schema)
	r.ev.check(id, err == nil, "reading the catalogue: %v", err)
	for _, t := range tables {
		if _, ok := doc.Tables[t]; !ok && !officialStateTable(t, r.db.Schema) {
			r.ev.fail(id, "table %s is not declared in lifecycle.yaml (P11.11; a runtime's own migration-state table is class platform)", t)
		}
	}
}
