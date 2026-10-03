package compconf

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/brickKit/be-acceptance/conformance/component/infra"
	beprotocol "github.com/brickKit/be-protocol"
	"github.com/jackc/pgx/v5"
)

// authzProjectionTables are created only in schemas whose component declares resources
// (ddl/07, ruled for rc.2).
var authzProjectionTables = map[string]bool{"besdk_authz_acl": true, "besdk_authz_cursor": true}

// CP-DB-04: the besdk_* tables of the migrated schema match the reference DDL column for column
// (name, position, type, nullability, default), with the same primary key and partitioning;
// the platform functions exist (P11.3).
func caseDB04(ctx context.Context, r *Run) {
	const id = "CP-DB-04"
	if !r.needDB(id) {
		return
	}
	conn, err := r.dbConn(ctx)
	if !r.ev.require(id, err == nil, "connecting: %v", err) {
		return
	}
	defer conn.Close(ctx)
	ref := "ccref_" + infra.RandHex(4)
	if err := applyReferenceDDL(ctx, conn, ref); !r.ev.require(id, err == nil, "applying the reference DDL: %v", err) {
		return
	}
	defer func() { _, _ = conn.Exec(context.Background(), "DROP SCHEMA "+qi(ref)+" CASCADE") }()
	want, err := describeTables(ctx, conn, ref)
	if !r.ev.require(id, err == nil, "describing the reference: %v", err) {
		return
	}
	got, err := describeTables(ctx, conn, r.db.Schema)
	if !r.ev.require(id, err == nil, "describing the schema: %v", err) {
		return
	}
	hasResources := len(r.comp.Assembly.Resources) > 0
	for _, t := range sortedKeys(want) {
		g, present := got[t]
		switch {
		case authzProjectionTables[t] && !hasResources:
			r.ev.check(id, !present, "%s exists although assembly.yaml declares no resources (ddl/07: projection tables only in an owner of resource types)", t)
		case !present:
			r.ev.fail(id, "%s is missing (P11.3: the platform migration creates every besdk_* table)", t)
		default:
			for _, d := range diffLines(want[t], g) {
				r.ev.fail(id, "%s: %s (P11.3: column for column as ddl/)", t, d)
			}
		}
	}
	for _, t := range sortedKeys(got) {
		r.ev.check(id, want[t] != nil, "%s is not in the reference DDL", t)
	}
	fns, err := queryStrings(ctx, conn, `SELECT p.proname FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
	    WHERE n.nspname = $1 AND p.proname LIKE 'besdk\_%' EXCEPT SELECT p.proname FROM pg_proc p
	    JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = $2`, ref, r.db.Schema)
	r.ev.check(id, err == nil && len(fns) == 0, "platform functions missing from the schema: %v %v (P11.3, P10.12)", fns, err)
}

// applyReferenceDDL runs ddl/[0-9]*.sql of the pinned be-protocol in a fresh schema.
func applyReferenceDDL(ctx context.Context, conn *pgx.Conn, schema string) error {
	files, err := fs.Glob(beprotocol.FS, "ddl/[0-9]*.sql")
	if err != nil || len(files) == 0 {
		return fmt.Errorf("no reference DDL in be-protocol: %v", err)
	}
	sort.Strings(files)
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+qi(schema)); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, "SET search_path TO "+qi(schema)); err != nil {
		return err
	}
	defer func() { _, _ = conn.Exec(context.Background(), "RESET search_path") }()
	for _, f := range files {
		b, _ := fs.ReadFile(beprotocol.FS, f)
		if _, err := conn.Exec(ctx, string(b), pgx.QueryExecModeSimpleProtocol); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
	}
	return nil
}

// describeTables describes every besdk_* table of a schema that is not a partition: kind,
// columns in order, primary key.
func describeTables(ctx context.Context, conn *pgx.Conn, schema string) (map[string][]string, error) {
	rows, err := conn.Query(ctx, `
	  SELECT c.relname, 'kind ' || c.relkind::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	   WHERE n.nspname = $1 AND c.relkind IN ('r','p') AND NOT c.relispartition AND c.relname LIKE 'besdk\_%'
	  UNION ALL
	  SELECT col.table_name, 'col ' || lpad(col.ordinal_position::text, 3, '0') || ' ' || col.column_name || ' ' ||
	         col.data_type || ' ' || col.udt_name || ' null=' || col.is_nullable || ' default=' || coalesce(col.column_default, '')
	    FROM information_schema.columns col JOIN pg_class c ON c.relname = col.table_name
	    JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = col.table_schema
	   WHERE col.table_schema = $1 AND col.table_name LIKE 'besdk\_%' AND NOT c.relispartition
	  UNION ALL
	  SELECT c.relname, 'pk ' || string_agg(a.attname, ',' ORDER BY array_position(i.indkey::int2[], a.attnum))
	    FROM pg_index i JOIN pg_class c ON c.oid = i.indrelid JOIN pg_namespace n ON n.oid = c.relnamespace
	    JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = ANY(i.indkey)
	   WHERE n.nspname = $1 AND i.indisprimary AND c.relname LIKE 'besdk\_%' AND NOT c.relispartition
	   GROUP BY c.relname`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var t, line string
		if err := rows.Scan(&t, &line); err != nil {
			return nil, err
		}
		out[t] = append(out[t], strings.ReplaceAll(line, schema+".", ""))
	}
	for t := range out {
		sort.Strings(out[t])
	}
	return out, rows.Err()
}

// diffLines lists what differs between two sorted descriptions.
func diffLines(want, got []string) []string {
	w, g := map[string]bool{}, map[string]bool{}
	for _, l := range want {
		w[l] = true
	}
	for _, l := range got {
		g[l] = true
	}
	var out []string
	for _, l := range want {
		if !g[l] {
			out = append(out, "missing or different: "+l)
		}
	}
	for _, l := range got {
		if !w[l] {
			out = append(out, "not in the reference: "+l)
		}
	}
	return out
}
