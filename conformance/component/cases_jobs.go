package compconf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/brickKit/be-acceptance/conformance/component/fakes"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// The jobs profile (P14): two replicas share the slots, leases and queue rows of the schema.
// The suite's JOBS_OVERRIDES runs be.cleanup every 2 s and be.lifecycle every 3 s.

// stepSecondReplica is a setup step: a second serving instance with the same configuration.
func stepSecondReplica(ctx context.Context, r *Run) {
	if r.pg == nil || !r.mainUp || !contains(r.ran, "jobs") {
		return
	}
	in, err := r.startInstance(ctx, "replica", r.compEnv, fakes.NewLogs())
	if err == nil {
		_, err = waitStatus(ctx, in.base+"/readyz", 200, 60*time.Second)
	}
	if err != nil {
		r.ev.fail("CP-JOBS-01", "starting a second replica: %v", err)
		if in != nil {
			_ = in.c.Remove(context.Background())
			in.stop()
		}
		return
	}
	r.replica = in
}

// stepStopReplica is a setup step: the second replica goes away.
func stepStopReplica(_ context.Context, r *Run) {
	if r.replica != nil {
		_ = r.replica.c.Remove(context.Background())
		r.replica.stop()
		r.replica = nil
	}
}

// jobRuns sums be_job_runs_total{job} over results on one instance.
func jobRuns(ctx context.Context, base, job string) (float64, error) {
	x := send(ctx, nil, base, "GET", "/metrics")
	if x.Err != nil || x.Status != 200 {
		return 0, fmt.Errorf("/metrics = %d %v", x.Status, x.Err)
	}
	parser := expfmt.NewTextParser(model.UTF8Validation)
	fams, err := parser.TextToMetricFamilies(bytes.NewReader(x.Body))
	if err != nil {
		return 0, err
	}
	sum := 0.0
	for _, m := range fams["be_job_runs_total"].GetMetric() {
		for _, l := range m.GetLabel() {
			if l.GetName() == "job" && l.GetValue() == job {
				sum += m.GetCounter().GetValue()
			}
		}
	}
	return sum, nil
}

// CP-JOBS-01: with two replicas, each slot of be.cleanup (@every 2s) and of every fixtures cron
// job runs once: the runs both replicas count equal the slots claimed (P14.2, P14.5, P14.6).
func caseJobs01(ctx context.Context, r *Run) {
	const id = "CP-JOBS-01"
	if !r.needDB(id) || !r.ev.require(id, r.replica != nil, "no second replica") {
		return
	}
	if !r.ev.require(id, r.comp.HasConfigKey("JOBS_OVERRIDES"), "configSchema does not declare JOBS_OVERRIDES (P2.8, P14.5)") {
		return
	}
	jobs := []string{"be.cleanup"}
	for _, j := range r.comp.Fixtures.Jobs.Cron {
		jobs = append(jobs, j.Name)
	}
	conn, err := r.dbConn(ctx)
	if !r.ev.require(id, err == nil, "connecting: %v", err) {
		return
	}
	defer conn.Close(ctx)
	before := map[string]float64{}
	for _, j := range jobs {
		before[j] = r.runsOnBoth(ctx, j)
	}
	start := time.Now()
	time.Sleep(13 * time.Second)
	end := time.Now()
	time.Sleep(1500 * time.Millisecond) // let the last slot finish
	for _, j := range jobs {
		var slots int
		var holders int
		err := conn.QueryRow(ctx, `SELECT count(*), count(DISTINCT holder) FROM besdk_job_slot
		    WHERE name = $1 AND slot_at >= $2 AND slot_at < $3 AND done_at IS NOT NULL`, j, start, end).Scan(&slots, &holders)
		if !r.ev.check(id, err == nil, "reading besdk_job_slot: %v", err) {
			return
		}
		runs := r.runsOnBoth(ctx, j) - before[j]
		r.ev.check(id, slots >= 5, "%s ran %d slots in 13 s with the override @every 2s (P14.5, P14.6)", j, slots)
		r.ev.check(id, runs >= float64(slots) && runs <= float64(slots)+2,
			"%s: the two replicas counted %.0f runs for %d slots: a slot ran more than once, or runs are not counted (P14.2, P14.3)", j, runs, slots)
		r.ev.pass(id, fmt.Sprintf("%s %d slots, %.0f runs, %d holders", j, slots, runs, holders))
	}
}

func (r *Run) runsOnBoth(ctx context.Context, job string) float64 {
	a, _ := jobRuns(ctx, r.base, job)
	b, _ := jobRuns(ctx, r.replica.base, job)
	return a + b
}

type lease struct {
	holder  string
	epoch   int64
	expires time.Time
}

func readLease(ctx context.Context, conn *pgx.Conn, name string) (lease, error) {
	var l lease
	err := conn.QueryRow(ctx, `SELECT holder, epoch, expires_at FROM besdk_job_lease WHERE name = $1`, name).Scan(&l.holder, &l.epoch, &l.expires)
	return l, err
}

// CP-JOBS-02: the singleton be.lifecycle has one holder; when the holder stops (the suite
// freezes its container) another replica takes over within the TTL plus one renewal, with the
// epoch one higher (P14.2, P10.8).
func caseJobs02(ctx context.Context, r *Run) {
	const id = "CP-JOBS-02"
	if !r.needDB(id) || !r.ev.require(id, r.replica != nil, "no second replica") {
		return
	}
	conn, err := r.dbConn(ctx)
	if !r.ev.require(id, err == nil, "connecting: %v", err) {
		return
	}
	defer conn.Close(ctx)
	first, err := readLease(ctx, conn, "be.lifecycle")
	if !r.ev.check(id, err == nil, "no besdk_job_lease row for be.lifecycle (%v): the engine runs as a singleton (P16.2)", err) {
		return
	}
	for _, c := range []*instance{r.replica, {c: r.main}} {
		if err := c.c.Pause(ctx); !r.ev.require(id, err == nil, "docker pause: %v", err) {
			return
		}
		next, took := waitTakeover(ctx, conn, first, 45*time.Second)
		_ = c.c.Unpause(context.Background())
		if next.holder != first.holder {
			r.ev.check(id, next.epoch == first.epoch+1, "takeover from %s to %s moved the epoch %d → %d, want +1 (P14)", first.holder, next.holder, first.epoch, next.epoch)
			r.ev.pass(id, fmt.Sprintf("taken over in %v", took.Round(time.Second)))
			return
		}
	}
	r.ev.fail(id, "neither replica took the be.lifecycle lease over within 45 s of the other freezing (holder %s) (P14.2)", first.holder)
}

func waitTakeover(ctx context.Context, conn *pgx.Conn, first lease, d time.Duration) (lease, time.Duration) {
	start := time.Now()
	for time.Since(start) < d {
		l, err := readLease(ctx, conn, "be.lifecycle")
		if err == nil && l.holder != first.holder {
			return l, time.Since(start)
		}
		time.Sleep(time.Second)
	}
	return first, d
}

// CP-JOBS-05: the suite terminates every session of the runtime role while jobs run: both
// processes stay up and be.cleanup keeps claiming slots (P1.7, P14.2).
func caseJobs05(ctx context.Context, r *Run) {
	const id = "CP-JOBS-05"
	if !r.needDB(id) {
		return
	}
	for i := 0; i < 4; i++ {
		_ = r.superExec(ctx, r.db.Database, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = $1`, r.db.Runtime)
		time.Sleep(1500 * time.Millisecond)
	}
	after := time.Now()
	conn, err := r.dbConn(ctx)
	if !r.ev.require(id, err == nil, "connecting: %v", err) {
		return
	}
	defer conn.Close(ctx)
	ok := false
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline) && !ok; time.Sleep(time.Second) {
		var n int
		_ = conn.QueryRow(ctx, `SELECT count(*) FROM besdk_job_slot WHERE name = 'be.cleanup' AND slot_at > $1 AND done_at IS NOT NULL`, after).Scan(&n)
		ok = n > 0
	}
	r.ev.check(id, ok, "no be.cleanup slot ran within 20 s after its connections were terminated: the job was not restarted (P1.7)")
	for _, in := range []*instance{{c: r.main}, r.replica} {
		if in == nil || in.c == nil {
			continue
		}
		running, code, _ := in.c.Running(ctx)
		r.ev.check(id, running, "%s exited (%d) after its database sessions were terminated (P1.7)", in.c.Name, code)
	}
}

// CP-JOBS-03: a job enqueued by fixtures jobs.enqueues runs once across two replicas: its
// besdk_job_queue row ends done after one attempt (P14.2).
func caseJobs03(ctx context.Context, r *Run) {
	const id = "CP-JOBS-03"
	enq := r.comp.Fixtures.Jobs.Enqueues
	if len(enq) == 0 {
		r.ev.notApplicable(id, "fixtures enqueue no job")
		return
	}
	if !r.needDB(id) {
		return
	}
	conn, err := r.dbConn(ctx)
	if !r.ev.require(id, err == nil, "connecting: %v", err) {
		return
	}
	defer conn.Close(ctx)
	for _, e := range enq {
		res, opName := splitVia(e.Via)
		op, ok := r.comp.Fixtures.Resources[res][opName]
		if !r.ev.require(id, ok, "jobs.enqueues via %s is not a fixtures operation", e.Via) {
			continue
		}
		rid, _, err := r.create(ctx, res, "")
		if !r.ev.require(id, err == nil, "creating a record: %v", err) {
			continue
		}
		start := time.Now()
		x := r.invoke(ctx, op, rid, "", nil)
		if !r.ev.check(id, x.Status < 300, "%s = %d %s", e.Via, x.Status, x.reason()) {
			continue
		}
		var state string
		var attempts, rows int
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline) && state != "done"; time.Sleep(time.Second) {
			_ = conn.QueryRow(ctx, `SELECT count(*) OVER (), state, attempts FROM besdk_job_queue WHERE kind = $1 AND created_at >= $2
			    ORDER BY created_at LIMIT 1`, e.Kind, start.Add(-time.Second)).Scan(&rows, &state, &attempts)
		}
		r.ev.check(id, rows == 1 && state == "done" && attempts == 1,
			"%s after %s: %d rows, state %q, attempts %d, want one row done after 1 attempt (P14.2)", e.Kind, e.Via, rows, state, attempts)
	}
}

// CP-JOBS-04: an operation that enqueues a job, rolled back because the suite holds the row
// lock (fixtures lock on the same operation), leaves no job (P14.2).
func caseJobs04(ctx context.Context, r *Run) {
	const id = "CP-JOBS-04"
	lk := r.comp.Fixtures.Lock
	var kind string
	for _, e := range r.comp.Fixtures.Jobs.Enqueues {
		if lk != nil && e.Via == lk.While {
			kind = e.Kind
		}
	}
	if kind == "" {
		r.ev.notApplicable(id, "no fixtures lock on an operation that enqueues a job")
		return
	}
	if !r.needDB(id) {
		return
	}
	res, opName := splitVia(lk.While)
	op := r.comp.Fixtures.Resources[res][opName]
	rid, _, err := r.create(ctx, res, "")
	if !r.ev.require(id, err == nil, "creating a record: %v", err) {
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
	start := time.Now()
	_, _ = tx.Exec(ctx, lk.SQL, rid)
	x := r.invoke(ctx, op, rid, "", nil)
	_ = tx.Rollback(ctx)
	r.ev.require(id, x.Status == 409, "%s with the row locked = %d, want 409 so its transaction rolls back", lk.While, x.Status)
	time.Sleep(2 * time.Second)
	var n int
	_ = conn.QueryRow(ctx, `SELECT count(*) FROM besdk_job_queue WHERE kind = $1 AND created_at >= $2`, kind, start).Scan(&n)
	r.ev.check(id, n == 0, "%d %s jobs exist after the enqueuing transaction rolled back (P14.2)", n, kind)
}

// CP-JOBS-06: only when /_be/info lists job_run: two concurrent `job run <name>` of the first
// fixtures cron job (be.cleanup when none) with the in-process copy disabled run the slot once
// and both exit 0; an unknown job name exits 64 (P14.8, P1.1).
func caseJobs06(ctx context.Context, r *Run) {
	const id = "CP-JOBS-06"
	if r.lastInfo == nil && r.mainUp {
		_ = json.Unmarshal(r.call(ctx, "GET", "/_be/info").Body, &r.lastInfo)
	}
	caps, _ := r.lastInfo["capabilities"].([]any)
	if !containsAny(caps, "job_run") {
		r.ev.notApplicable(id, "/_be/info does not list job_run")
		return
	}
	name := "be.cleanup"
	if len(r.comp.Fixtures.Jobs.Cron) > 0 {
		name = r.comp.Fixtures.Jobs.Cron[0].Name
	}
	cmd := r.comp.Manifest.Migration.Command
	if !r.ev.require(id, len(cmd) > 0, "no entry point known (migration.command)") {
		return
	}
	env := envWith(r.compEnv, map[string]string{"JOBS_OVERRIDES": fmt.Sprintf(`{%q:{"enabled":false}}`, name)})
	conn, err := r.dbConn(ctx)
	if !r.ev.require(id, err == nil, "connecting: %v", err) {
		return
	}
	defer conn.Close(ctx)
	var before int
	_ = conn.QueryRow(ctx, `SELECT count(*) FROM besdk_job_slot WHERE name = $1`, name).Scan(&before)
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() {
			code, _, _ := r.runOnce(ctx, "jobrun", env, []string{cmd[0], "job", "run", name}, 2*time.Minute)
			codes <- code
		}()
	}
	for i := 0; i < 2; i++ {
		c := <-codes
		r.ev.check(id, c == 0, "job run %s exited %d, want 0 (P14.8)", name, c)
	}
	var after int
	_ = conn.QueryRow(ctx, `SELECT count(*) FROM besdk_job_slot WHERE name = $1`, name).Scan(&after)
	r.ev.check(id, after-before <= 1, "two concurrent job run %s claimed %d slots, want at most one (P14.8)", name, after-before)
	code, logs, _ := r.runOnce(ctx, "jobrun", r.compEnv, []string{cmd[0], "job", "run", "compconf.unknown"}, time.Minute)
	r.ev.check(id, code == 64, "job run of an unknown job exited %d, want 64 (P14.8): %.200s", code, logs)
}

func containsAny(xs []any, s string) bool {
	for _, x := range xs {
		if fmt.Sprint(x) == s {
			return true
		}
	}
	return false
}

