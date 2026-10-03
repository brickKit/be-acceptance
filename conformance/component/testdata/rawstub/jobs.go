package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// The background work of P14: cron jobs claim a slot in besdk_job_slot, singletons hold a
// lease in besdk_job_lease; both run supervised (P1.7), as the runtime role (P10.2).

type jobKind int

const (
	kindCron jobKind = iota
	kindSingleton
)

// job is one declared background job.
type job struct {
	name     string
	kind     jobKind
	sched    *schedule     // cron
	interval time.Duration // singleton: time between runs
	timeout  time.Duration // required (P14.2)
	run      func(context.Context) error
}

const leaseTTL = 30 * time.Second

// Scheduler runs the declared jobs of one process.
type Scheduler struct {
	a      *App
	holder string // <component ID>/<instance id>
	jobs   []job
}

func newScheduler(a *App) *Scheduler {
	s := &Scheduler{a: a, holder: a.cfg.ComponentID + "/" + newUUIDv7(time.Now())[24:]}
	cleanup, _ := parseSchedule("@every 1h")
	s.jobs = []job{
		{name: "be.cleanup", kind: kindCron, sched: cleanup, timeout: time.Minute, run: a.runCleanup},
		{name: "be.lifecycle", kind: kindSingleton, interval: time.Hour, timeout: time.Minute, run: a.runLifecycle},
	}
	known := map[string]bool{}
	for i := range s.jobs {
		j := &s.jobs[i]
		known[j.name] = true
		if o, ok := a.cfg.JobsOverrides[j.name]; ok {
			if o.Cron != nil && j.kind == kindCron {
				j.sched = o.Cron
			}
			if o.Interval > 0 && j.kind == kindSingleton {
				j.interval = o.Interval
			}
			if o.Enabled != nil && !*o.Enabled {
				j.run = nil
			}
		}
	}
	for name := range a.cfg.JobsOverrides {
		if !known[name] {
			a.log.Warn("jobs_override_unknown", F{"job": name}) // P14.5
		}
	}
	return s
}

// Run starts every enabled job under supervision.
func (s *Scheduler) Run(ctx context.Context) {
	for _, j := range s.jobs {
		if j.run == nil {
			continue
		}
		j := j
		switch j.kind {
		case kindCron:
			go supervise(ctx, s.a.log, j.name, func(ctx context.Context) { s.cronLoop(ctx, j) })
		case kindSingleton:
			go supervise(ctx, s.a.log, j.name, func(ctx context.Context) { s.singletonLoop(ctx, j) })
		}
	}
}

// cronLoop claims each slot with INSERT … ON CONFLICT DO NOTHING; whoever inserted runs it.
func (s *Scheduler) cronLoop(ctx context.Context, j job) {
	var tried time.Time
	for sleepCtx(ctx, 250*time.Millisecond) {
		slot := j.sched.last(time.Now(), s.a.cfg.BusinessTZ)
		if slot.Equal(tried) || slot.IsZero() {
			continue
		}
		won := false
		err := s.a.db.Tx(ctx, func(tx pgx.Tx) error {
			var one int
			err := tx.QueryRow(ctx, s.a.db.Q(`INSERT INTO besdk_job_slot (name, slot_at, holder) VALUES ($1, $2, $3)
				ON CONFLICT (name, slot_at) DO NOTHING RETURNING 1`), j.name, slot, s.holder).Scan(&one)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			won = err == nil
			return err
		})
		if err != nil {
			s.a.log.Warn("job_claim_failed", F{"job": j.name, "error": rootCause(err).Error()})
			continue // the slot is tried again on the next tick
		}
		tried = slot
		if !won {
			continue
		}
		result := s.runOnce(ctx, j)
		_ = s.a.db.Tx(ctx, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, s.a.db.Q(`UPDATE besdk_job_slot SET done_at = now(), result = $3 WHERE name = $1 AND slot_at = $2`),
				j.name, slot, result)
			return err
		})
	}
}

// singletonLoop takes or renews the lease every TTL/3 and runs the job every interval while
// it holds it.
func (s *Scheduler) singletonLoop(ctx context.Context, j job) {
	var last time.Time
	for {
		held, err := s.takeLease(ctx, j.name)
		if err != nil {
			s.a.log.Warn("job_lease_failed", F{"job": j.name, "error": rootCause(err).Error()})
		}
		if held && time.Since(last) >= j.interval {
			last = time.Now()
			s.runOnce(ctx, j)
		}
		wait := min(leaseTTL/3, j.interval)
		if !sleepCtx(ctx, wait) {
			return
		}
	}
}

// takeLease is the statement of P14 ("singleton").
func (s *Scheduler) takeLease(ctx context.Context, name string) (bool, error) {
	held := false
	err := s.a.db.Tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, s.a.db.Q(`INSERT INTO besdk_job_lease (name, holder, expires_at) VALUES ($1, $2, now() - interval '1 second')
			ON CONFLICT (name) DO NOTHING`), name, s.holder); err != nil {
			return err
		}
		var epoch int64
		err := tx.QueryRow(ctx, s.a.db.Q(`UPDATE besdk_job_lease
			SET holder = $2, epoch = CASE WHEN holder = $2 THEN epoch ELSE epoch + 1 END, expires_at = now() + $3::interval
			WHERE name = $1 AND (expires_at < now() OR holder = $2) RETURNING epoch`), name, s.holder, leaseTTL.String()).Scan(&epoch)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		held = err == nil
		return err
	})
	return held, err
}

// runOnce runs a job with its timeout and records the metrics; the result is ok or error.
func (s *Scheduler) runOnce(ctx context.Context, j job) string {
	rctx, cancel := context.WithTimeout(ctx, j.timeout)
	defer cancel()
	start := time.Now()
	err := j.run(rctx)
	m := s.a.metrics
	m.Observe("be_job_duration_seconds", time.Since(start).Seconds(), j.name)
	if err != nil {
		m.Inc("be_job_runs_total", j.name, "error")
		s.a.log.Warn("job_failed", F{"job": j.name, "error": rootCause(err).Error()})
		return "error"
	}
	m.Inc("be_job_runs_total", j.name, "ok")
	m.Set("be_job_last_success_timestamp_seconds", float64(time.Now().Unix()), j.name)
	return "ok"
}

// runCleanup is be.cleanup: old slot rows and expired idempotency keys (P14.7, P13.7).
func (a *App) runCleanup(ctx context.Context) error {
	return a.db.Tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, a.db.Q(`DELETE FROM besdk_job_slot WHERE done_at < now() - interval '7 days'`)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, a.db.Q(`DELETE FROM besdk_idempotency WHERE expires_at < now()`))
		return err
	})
}

// runLifecycle is be.lifecycle: keep besdk_outbox's partition window (P16.6) through the
// platform function, under a step lock (P16.2, P10.8). The stub declares no partitioned table
// of its own and has no cold store, so this is all its engine does.
func (a *App) runLifecycle(ctx context.Context) error {
	return a.db.Tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, a.db.Q(`SELECT pg_advisory_xact_lock(hashtext(current_schema() || ':be.lifecycle'), hashtext('besdk_outbox'))`)); err != nil {
			return err
		}
		return ensureOutboxWindow(ctx, tx, a.db.prefix, time.Now())
	})
}
