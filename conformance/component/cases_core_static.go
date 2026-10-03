package compconf

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	beprotocol "github.com/brickKit/be-protocol"
	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"
)

// CP-CORE-12: component.yaml declares both checks, the stop grace period and port protocols.
func caseCore12(_ context.Context, r *Run) {
	const id = "CP-CORE-12"
	m := r.comp.Manifest
	r.ev.check(id, m.HealthCheck.Type == "http" && m.HealthCheck.Path == "/healthz",
		"healthCheck = %+v, want {type: http, path: /healthz} (P1.11)", m.HealthCheck)
	r.ev.check(id, m.ReadinessCheck.Type == "http" && m.ReadinessCheck.Path == "/readyz",
		"readinessCheck = %+v, want {type: http, path: /readyz} (P1.11)", m.ReadinessCheck)
	grace := r.shutdownGrace()
	r.ev.check(id, time.Duration(m.Deployment.StopGracePeriodSeconds)*time.Second >= grace+5*time.Second,
		"deployment.stopGracePeriodSeconds = %d, want >= SHUTDOWN_GRACE (%v) + 5 s (P1.12)", m.Deployment.StopGracePeriodSeconds, grace)
	r.ev.check(id, m.Deployment.Protocol == "http", "deployment.protocol = %q, want http (P7.14)", m.Deployment.Protocol)
	for _, p := range m.Deployment.ExtraPorts {
		if p.Name == "grpc" {
			r.ev.check(id, p.Protocol == "grpc", "extra port grpc has protocol %q, want grpc (P7.14)", p.Protocol)
		} else {
			r.ev.check(id, p.Protocol == "http" || p.Protocol == "grpc" || p.Protocol == "tcp",
				"extra port %s has protocol %q (P7.14)", p.Name, p.Protocol)
		}
	}
}

// shutdownGrace is SHUTDOWN_GRACE as the component is configured (default 25 s).
func (r *Run) shutdownGrace() time.Duration {
	if p, ok := r.comp.Manifest.ConfigSchema.Properties["SHUTDOWN_GRACE"]; ok && p.Default != nil {
		if d, err := time.ParseDuration(formatDefault(p.Default)); err == nil {
			return d
		}
	}
	return 25 * time.Second
}

// CP-CORE-14 (declarations): every secret is mount: file and named _FILE, and only those.
func caseCore14Static(_ context.Context, r *Run) {
	const id = "CP-CORE-14"
	for _, k := range sortedKeys(r.comp.Manifest.ConfigSchema.Properties) {
		p := r.comp.Manifest.ConfigSchema.Properties[k]
		file := strings.HasSuffix(k, "_FILE")
		r.ev.check(id, p.Secret == file && (p.Mount == "file") == p.Secret,
			"configSchema %s: secret=%v mount=%q; a key is secret exactly when it is mount: file and named _FILE (P2.12)",
			k, p.Secret, p.Mount)
	}
}

// CP-CORE-01: the migration entry point run twice exits 0 both times and the second run
// changes nothing; an argument the entry point does not know exits 64 before reading config.
func caseCore01(ctx context.Context, r *Run) {
	const id = "CP-CORE-01"
	cmd := r.comp.Manifest.Migration.Command
	if !r.ev.check(id, len(cmd) > 0, "component.yaml has no migration.command (P1.1)") {
		return
	}
	var snaps [2]string
	for i := 0; i < 2; i++ {
		code, logs, err := r.runOnce(ctx, "migrate", r.compEnv, cmd, 2*time.Minute)
		if !r.ev.check(id, err == nil && code == 0, "migration run %d exited %d (%v); output: %.400s", i+1, code, err, logs) {
			return
		}
		if r.pg != nil {
			s, err := r.schemaSnapshot(ctx)
			if !r.ev.check(id, err == nil, "reading the schema after run %d: %v", i+1, err) {
				return
			}
			snaps[i] = s
		}
	}
	r.ev.check(id, snaps[0] == snaps[1], "the second migration run changed the schema or its rows (P1.1)")
	code, logs, err := r.runOnce(ctx, "badarg", nil, []string{cmd[0], "compconf-unknown-argument"}, time.Minute)
	r.ev.check(id, err == nil && code == 64,
		"[%s compconf-unknown-argument] with no configuration exited %d (%v), want 64 at once (P1.1); output: %.300s", cmd[0], code, err, logs)
}

// schemaSnapshot describes the component's schema: columns, indexes, constraints and the row
// count of every table.
func (r *Run) schemaSnapshot(ctx context.Context) (string, error) {
	conn, err := pgx.Connect(ctx, r.pg.HostDSN(r.db.Database))
	if err != nil {
		return "", err
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, `
	  SELECT 'col '||table_name||'.'||column_name||' '||data_type||' '||is_nullable||' '||coalesce(column_default,'')
	    FROM information_schema.columns WHERE table_schema = $1
	  UNION ALL SELECT 'idx '||indexname||' '||indexdef FROM pg_indexes WHERE schemaname = $1
	  UNION ALL SELECT 'con '||conname FROM pg_constraint c JOIN pg_namespace n ON n.oid = c.connamespace WHERE n.nspname = $1`, r.db.Schema)
	if err != nil {
		return "", err
	}
	var lines []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return "", err
		}
		lines = append(lines, s)
	}
	rows.Close()
	tables, err := conn.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname = $1`, r.db.Schema)
	if err != nil {
		return "", err
	}
	var names []string
	for tables.Next() {
		var t string
		_ = tables.Scan(&t)
		names = append(names, t)
	}
	tables.Close()
	for _, t := range names {
		var n int64
		q := "SELECT count(*) FROM " + pgx.Identifier{r.db.Schema, t}.Sanitize()
		if err := conn.QueryRow(ctx, q).Scan(&n); err != nil {
			return "", err
		}
		lines = append(lines, fmt.Sprintf("rows %s %d", t, n))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n"), nil
}

type configKey struct {
	Name   string `yaml:"name"`
	Format string `yaml:"format"`
}

// typedKey picks a declared protocol key whose value must parse (int, duration, bool, enum).
func (r *Run) typedKey() (string, bool) {
	b, err := fs.ReadFile(beprotocol.FS, "schemas/config-keys.yaml")
	if err != nil {
		return "", false
	}
	var cat struct {
		Keys []configKey `yaml:"keys"`
	}
	_ = yaml.Unmarshal(b, &cat)
	formats := map[string]string{}
	for _, k := range cat.Keys {
		formats[k.Name] = k.Format
	}
	for _, pref := range []string{"SHUTDOWN_GRACE", "HTTP_DEFAULT_TIMEOUT", "LOG_LEVEL"} {
		if r.comp.HasConfigKey(pref) {
			return pref, true
		}
	}
	for _, k := range sortedKeys(r.comp.Manifest.ConfigSchema.Properties) {
		switch formats[k] {
		case "int", "duration", "bool", "enum", "durations":
			return k, true
		}
	}
	return "", false
}

// CP-CORE-02: a missing required key and an unparsable value exit 78, each named on its own
// log line, all reported together; so does a COMPONENT_ID other than the component's.
func caseCore02(ctx context.Context, r *Run) {
	const id = "CP-CORE-02"
	req := r.comp.Manifest.ConfigSchema.Required
	bad, hasBad := r.typedKey()
	set := map[string]string{}
	var named []string
	if hasBad {
		set[bad] = "compconf-not-valid"
		named = append(named, bad)
	}
	var del []string
	if len(req) > 0 {
		del = append(del, req[0])
		named = append(named, req[0])
	}
	if !r.ev.check(id, len(named) > 0, "the component declares no required key and no typed key to break") {
		return
	}
	code, logs, err := r.runOnce(ctx, "badcfg", envWith(r.compEnv, set, del...), nil, time.Minute)
	r.ev.check(id, err == nil && code == 78, "missing %v / unparsable %s: exited %d (%v), want 78 (P1.2)", del, bad, code, err)
	for _, k := range named {
		r.ev.check(id, jsonLineNaming(logs, k), "no JSON log line names %s (P1.2, P2.3: one line per problem, all together)", k)
	}
	code, logs, err = r.runOnce(ctx, "badid", envWith(r.compEnv, map[string]string{"COMPONENT_ID": "compconf/other"}), nil, time.Minute)
	r.ev.check(id, err == nil && code == 78, "COMPONENT_ID=compconf/other: exited %d (%v), want 78 (P1.2)", code, err)
	r.ev.check(id, jsonLineNaming(logs, "COMPONENT_ID"), "no JSON log line names COMPONENT_ID (P1.2)")
}

func jsonLineNaming(logs, key string) bool {
	for _, l := range strings.Split(logs, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "{") && strings.Contains(l, key) {
			return true
		}
	}
	return false
}
