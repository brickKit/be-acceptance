package compconf

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brickKit/be-acceptance/conformance/component/fakes"
	"github.com/brickKit/be-acceptance/conformance/component/infra"
	"github.com/jackc/pgx/v5"
)

// The db profile: identity, locks, pool, platform tables, owner never online, password
// rotation (P10, P11.3, P2.9).

func qi(s string) string { return pgx.Identifier{s}.Sanitize() }

// dbConn is a superuser connection to the component's database with search_path = its schema.
func (r *Run) dbConn(ctx context.Context) (*pgx.Conn, error) {
	conn, err := pgx.Connect(ctx, r.pg.HostDSN(r.db.Database))
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, "SET search_path TO "+qi(r.db.Schema)); err != nil {
		conn.Close(ctx)
		return nil, err
	}
	return conn, nil
}

func (r *Run) needDB(id string) bool {
	if r.pg == nil {
		r.ev.notApplicable(id, "the component has no database")
		return false
	}
	return r.needMain(id)
}

func queryStrings(ctx context.Context, conn *pgx.Conn, sql string, args ...any) ([]string, error) {
	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

var uuidV7Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// CP-DB-01: under the run's random identity the component works; every object in the schema
// belongs to PG_OWNER_USER; neither role owns anything elsewhere; the runtime role is not the
// owner's member and cannot create; own IDs are UUIDv7 (P10.1, P10.2, P10.7, P11.2, P11.5).
func caseDB01(ctx context.Context, r *Run) {
	const id = "CP-DB-01"
	if !r.needDB(id) {
		return
	}
	res, rid, err := r.createAny(ctx)
	if r.ev.check(id, err == nil, "creating a record under the random identity: %v", err) {
		r.ev.check(id, uuidV7Re.MatchString(rid), "%s create returned id %q, not a lowercase UUIDv7 (P11.5)", res, rid)
	}
	conn, err := r.dbConn(ctx)
	if !r.ev.require(id, err == nil, "connecting as the suite: %v", err) {
		return
	}
	defer conn.Close(ctx)
	bad, err := queryStrings(ctx, conn, `
	  SELECT c.relkind::text || ' ' || c.relname || ' owned by ' || pg_get_userbyid(c.relowner)
	    FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	   WHERE n.nspname = $1 AND c.relkind IN ('r','p','S','v','m','f') AND pg_get_userbyid(c.relowner) <> $2
	  UNION ALL
	  SELECT 'function ' || p.proname || ' owned by ' || pg_get_userbyid(p.proowner)
	    FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
	   WHERE n.nspname = $1 AND pg_get_userbyid(p.proowner) <> $2`, r.db.Schema, r.db.Owner)
	r.ev.check(id, err == nil, "reading the catalogue: %v", err)
	for _, b := range bad {
		r.ev.fail(id, "%s in the component's schema, want PG_OWNER_USER (P10.1, P11.2)", b)
	}
	outside, err := queryStrings(ctx, conn, `
	  SELECT 'relation ' || n.nspname || '.' || c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	   WHERE pg_get_userbyid(c.relowner) IN ($2, $3) AND n.nspname <> $1 AND n.nspname <> 'pg_toast'
	  UNION ALL
	  SELECT 'function ' || n.nspname || '.' || p.proname FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
	   WHERE pg_get_userbyid(p.proowner) IN ($2, $3) AND n.nspname <> $1
	  UNION ALL
	  SELECT 'schema ' || nspname FROM pg_namespace WHERE pg_get_userbyid(nspowner) IN ($2, $3) AND nspname <> $1`,
		r.db.Schema, r.db.Owner, r.db.Runtime)
	r.ev.check(id, err == nil, "reading the catalogue: %v", err)
	for _, o := range outside {
		r.ev.fail(id, "%s belongs to the component's roles outside its schema (P11.2: nothing outside the schema)", o)
	}
	var member, create bool
	err = conn.QueryRow(ctx, `SELECT pg_has_role($1, $2, 'MEMBER'), has_schema_privilege($1, $3, 'CREATE')`,
		r.db.Runtime, r.db.Owner, r.db.Schema).Scan(&member, &create)
	r.ev.check(id, err == nil && !member && !create, "runtime role: member of the owner %v, CREATE on the schema %v (%v) (P10.1, P11.2)", member, create, err)
}

// CP-DB-02: while the suite holds the row lock of fixtures lock, the operation answers 409
// LOCK_TIMEOUT within lock_timeout (2 s) plus a margin (P10.3, P10.4, P3.4).
func caseDB02(ctx context.Context, r *Run) {
	const id = "CP-DB-02"
	if !r.needDB(id) {
		return
	}
	lk := r.comp.Fixtures.Lock
	if lk == nil {
		r.ev.notApplicable(id, "fixtures have no lock")
		return
	}
	res, opName := splitVia(lk.While)
	op, ok := r.comp.Fixtures.Resources[res][opName]
	if !r.ev.require(id, ok, "lock.while %s is not a fixtures operation", lk.While) {
		return
	}
	rid, _, err := r.create(ctx, res, "")
	if !r.ev.require(id, err == nil, "creating the record to lock: %v", err) {
		return
	}
	conn, err := r.dbConn(ctx)
	if !r.ev.require(id, err == nil, "connecting: %v", err) {
		return
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if !r.ev.require(id, err == nil, "BEGIN: %v", err) {
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, lk.SQL, rid); !r.ev.require(id, err == nil, "lock.sql: %v", err) {
		return
	}
	x := r.invoke(ctx, op, rid, "", nil)
	r.ev.check(id, x.Status == 409 && x.reason() == "LOCK_TIMEOUT",
		"%s %s while the row is locked = %d %s, want 409 LOCK_TIMEOUT (P10.4)", op.Method, op.Path, x.Status, x.reason())
	r.ev.check(id, x.Took < 4*time.Second, "answered after %v, want within lock_timeout 2 s plus 2 s (P10.3)", x.Took.Round(100*time.Millisecond))
}

// splitVia splits resources.<res>.<op>.
func splitVia(via string) (string, string) {
	rest := strings.TrimPrefix(via, "resources.")
	i := strings.LastIndex(rest, ".")
	if i < 0 {
		return rest, ""
	}
	return rest[:i], rest[i+1:]
}

// poolMax is PG_POOL_MAX as configured for the run.
func (r *Run) envValue(k string) string {
	for _, kv := range r.compEnv {
		if strings.HasPrefix(kv, k+"=") {
			return kv[len(k)+1:]
		}
	}
	return ""
}

// CP-DB-03: PG_POOL_MAX + 10 concurrent requests that hold a connection: the runtime role's
// sessions and those with application_name = the component ID never exceed PG_POOL_MAX (P10.5).
func caseDB03(ctx context.Context, r *Run) {
	const id = "CP-DB-03"
	if !r.needDB(id) {
		return
	}
	max, err := strconv.Atoi(r.envValue("PG_POOL_MAX"))
	if !r.ev.require(id, err == nil && max > 0, "PG_POOL_MAX is not configured (P2.8)") {
		return
	}
	load, what := r.dbLoad(ctx)
	if load == nil {
		r.ev.fail(id, "fixtures give no operation that reaches the database (slow with via=db, or a list)")
		return
	}
	conn, err := pgx.Connect(ctx, r.pg.HostDSN(r.db.Database))
	if !r.ev.require(id, err == nil, "connecting: %v", err) {
		return
	}
	defer conn.Close(ctx)
	var wg sync.WaitGroup
	for i := 0; i < max+10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); load() }()
	}
	peakUser, peakApp := 0, 0
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	for sampling := true; sampling; {
		var u, a int
		_ = conn.QueryRow(ctx, `SELECT count(*) FILTER (WHERE usename = $1), count(*) FILTER (WHERE application_name = $2)
		    FROM pg_stat_activity WHERE datname = current_database()`, r.db.Runtime, r.comp.ID()).Scan(&u, &a)
		peakUser, peakApp = max2(peakUser, u), max2(peakApp, a)
		select {
		case <-done:
			sampling = false
		case <-time.After(50 * time.Millisecond):
		}
	}
	r.ev.check(id, peakUser <= max, "%d sessions of the runtime role under %d concurrent %s, PG_POOL_MAX %d (P10.5)", peakUser, max+10, what, max)
	r.ev.check(id, peakApp <= max, "%d sessions with application_name %s, PG_POOL_MAX %d (P10.5)", peakApp, r.comp.ID(), max)
	time.Sleep(time.Second)
	var idle int
	named := r.comp.ID() + "@" + r.comp.Version()
	_ = conn.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND application_name = $1`, named).Scan(&idle)
	r.ev.check(id, idle >= 1, "no session named %s after the load: the pool must keep its last connection under its versioned name (P10.2, P10.5)", named)
	r.ev.pass(id, fmt.Sprintf("peak %d sessions (%d by application_name) of %d", peakUser, peakApp, max))
}

func max2(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// dbLoad is a request that holds a database connection for a while: slow with via=db when the
// slow operation takes a via parameter (the widget's convention), else a list.
func (r *Run) dbLoad(ctx context.Context) (func(), string) {
	if s := r.comp.Fixtures.Slow; s != nil && s.Path != "" {
		if _, ok := s.Query["via"]; ok {
			id := r.slowRecord(ctx)
			return func() { r.invoke(ctx, *s, id, "", map[string]string{"ms": "1500", "via": "db"}) }, "slow?via=db&ms=1500"
		}
	}
	for _, res := range r.creatable() {
		if l, ok := r.comp.Fixtures.Resources[res]["list"]; ok && l.Path != "" {
			return func() {
				for i := 0; i < 5; i++ {
					r.invoke(ctx, l, "", "", nil)
				}
			}, res + " list ×5"
		}
	}
	return nil, ""
}

// slowRecord is a record for a slow path with {id}, created once.
func (r *Run) slowRecord(ctx context.Context) string {
	if s := r.comp.Fixtures.Slow; s != nil && strings.Contains(s.Path, "{id}") {
		if r.slowID == "" {
			_, r.slowID, _ = r.createAny(ctx)
		}
		return r.slowID
	}
	return ""
}

// CP-DB-05: while the service runs no session is logged in as PG_OWNER_USER, and the
// lifecycle engine keeps the outbox's partitions ahead without it: a dropped future partition
// comes back (P10.12, P10.1; the suite sets be.lifecycle to a short interval).
func caseDB05(ctx context.Context, r *Run) {
	const id = "CP-DB-05"
	if !r.needDB(id) {
		return
	}
	conn, err := r.dbConn(ctx)
	if !r.ev.require(id, err == nil, "connecting: %v", err) {
		return
	}
	defer conn.Close(ctx)
	r.sampleOwner(ctx, conn, id, 3*time.Second)
	parts, err := partitions(ctx, conn, "besdk_outbox")
	if !r.ev.check(id, err == nil && len(parts) > 0, "besdk_outbox has no partition (%v) (P11.3, P16.6)", err) {
		return
	}
	now := time.Now()
	var ahead *partition
	for i := range parts {
		if parts[i].from.After(now) && (ahead == nil || parts[i].from.After(ahead.from)) {
			ahead = &parts[i]
		}
	}
	if !r.ev.check(id, ahead != nil, "besdk_outbox has no partition ahead of now (partitions %v): the window must cover ahead (P16.6)", parts) {
		return
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf("DROP TABLE %s", qi(ahead.name))); !r.ev.require(id, err == nil, "dropping %s: %v", ahead.name, err) {
		return
	}
	back := false
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline) && !back; time.Sleep(time.Second) {
		ps, _ := partitions(ctx, conn, "besdk_outbox")
		for _, p := range ps {
			back = back || !p.from.After(ahead.from) && p.to.After(ahead.from)
		}
		r.sampleOwner(ctx, conn, id, 0)
	}
	r.ev.check(id, back, "the dropped partition %s (from %s) was not re-created within 30 s with be.lifecycle every 3 s (P16.2, P16.6)", ahead.name, ahead.from.Format(time.RFC3339))
}

// sampleOwner records a failure when a session of PG_OWNER_USER exists, sampling for d.
func (r *Run) sampleOwner(ctx context.Context, conn *pgx.Conn, id string, d time.Duration) {
	end := time.Now().Add(d)
	for {
		var n int
		var app string
		_ = conn.QueryRow(ctx, `SELECT count(*), coalesce(string_agg(DISTINCT application_name, ','), '')
		    FROM pg_stat_activity WHERE usename = $1`, r.db.Owner).Scan(&n, &app)
		if n > 0 {
			r.ev.fail(id, "%d sessions logged in as PG_OWNER_USER while the service runs (application_name %q) (P10.12)", n, app)
			return
		}
		if time.Now().After(end) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

type partition struct {
	name     string
	from, to time.Time
}

var rangeBound = regexp.MustCompile(`FROM \('([^']+)'\) TO \('([^']+)'\)`)

// partitions lists the range partitions of a table of the schema with their bounds.
func partitions(ctx context.Context, conn *pgx.Conn, parent string) ([]partition, error) {
	rows, err := conn.Query(ctx, `SELECT c.relname, pg_get_expr(c.relpartbound, c.oid)
	    FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
	   WHERE i.inhparent = to_regclass($1)`, parent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []partition
	for rows.Next() {
		var name, bound string
		if err := rows.Scan(&name, &bound); err != nil {
			return nil, err
		}
		m := rangeBound.FindStringSubmatch(bound)
		if m == nil {
			continue
		}
		p := partition{name: name}
		p.from, _ = parsePGTime(m[1])
		p.to, _ = parsePGTime(m[2])
		out = append(out, p)
	}
	return out, rows.Err()
}

func parsePGTime(s string) (time.Time, error) {
	for _, l := range []string{"2006-01-02 15:04:05-07", "2006-01-02 15:04:05.999999-07", "2006-01-02 15:04:05-07:00", "2006-01-02"} {
		if t, err := time.Parse(l, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unparsable bound %q", s)
}

// CP-DB-06: PG_USER's password changes and its file is replaced while the service runs: after
// at most 30 s the new value applies to new connections, without a restart and without a
// failed request; one INFO line names the key (P2.9, P10.5).
func caseDB06(ctx context.Context, r *Run) {
	const id = "CP-DB-06"
	if !r.needDB(id) {
		return
	}
	probe := r.dbProbe(ctx)
	if probe == nil {
		r.ev.fail(id, "fixtures give no operation that reaches the database")
		return
	}
	newPW := infra.RandHex(14)
	if err := r.superExec(ctx, r.db.Database, fmt.Sprintf("ALTER ROLE %s PASSWORD '%s'", qi(r.db.Runtime), newPW)); !r.ev.require(id, err == nil, "ALTER ROLE: %v", err) {
		return
	}
	file := filepath.Join(r.runDir, "secrets", r.comp.ServiceName(), "PG_PASSWORD_FILE")
	if err := os.WriteFile(file, []byte(newPW+"\n"), 0o644); !r.ev.require(id, err == nil, "replacing the file: %v", err) {
		return
	}
	r.oldSecrets = append(r.oldSecrets, r.secrets["PG_PASSWORD_FILE"])
	r.secrets["PG_PASSWORD_FILE"], r.db.RuntimePassword = newPW, newPW
	changed := time.Now()
	for time.Since(changed) < 32*time.Second {
		if st := probe(); st >= 500 {
			r.ev.fail(id, "a request failed with %d %.0fs after the password changed (P2.9: no failed request)", st, time.Since(changed).Seconds())
		}
		time.Sleep(2 * time.Second)
	}
	_ = r.superExec(ctx, r.db.Database, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = $1`, r.db.Runtime)
	time.Sleep(500 * time.Millisecond)
	for i := 0; i < 5; i++ {
		st := probe()
		r.ev.check(id, st > 0 && st < 500, "after the old connections were closed a request answered %d: new connections do not use the new password (P2.9)", st)
	}
	lines := r.logs.Find(func(l fakes.LogLine) bool {
		return l.At.After(changed) && l.Str("level") == "info" && strings.Contains(l.Raw, "PG_PASSWORD_FILE")
	})
	r.ev.check(id, len(lines) >= 1, "no INFO line naming PG_PASSWORD_FILE after the file changed (P2.9)")
	r.ev.pass(id)
}

// dbProbe is one request that needs the database: the list of a resource the fixtures can
// create (a resource without a create, such as reference rows, may not touch the database).
// It returns the status (0 on a transport error).
func (r *Run) dbProbe(ctx context.Context) func() int {
	for _, res := range r.creatable() {
		if l, ok := r.comp.Fixtures.Resources[res]["list"]; ok && l.Path != "" {
			return func() int { return r.invoke(ctx, l, "", "", nil).Status }
		}
	}
	if res := r.creatable(); len(res) > 0 {
		return func() int {
			if _, x, _ := r.create(ctx, res[0], ""); x != nil {
				return x.Status
			}
			return 0
		}
	}
	return nil
}
