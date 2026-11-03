package probe

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/errs"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/work"
	"github.com/jackc/pgx/v5"
)

// The probe jobs (since 3.2.30, Go; the Python probe follows in the foundations' item 6): one queue job per effect
// class, and the steps singleton when PROBE_STEPS_EVERY is set. Every attempt leaves a row in probe_job_attempts, so
// the suite's background work profile sees from outside what each claim did.
const (
	JobAttempts  = 3
	MaxJobHoldMS = 120_000
	MaxSteps     = 20
)

var jobNames = map[string]string{"local": "probe.job.local", "idempotent_external": "probe.job.idempotent",
	"non_idempotent_external": "probe.job.non_idempotent"}

type jobIn struct {
	Effect string `json:"effect"` // local (default), idempotent_external, non_idempotent_external
	// HoldMS: the first attempt waits this long before it commits its effect (an external job after its call), long
	// enough for the suite to stop or kill the probe, or to outdate the claim, meanwhile; later attempts do not wait
	HoldMS    int    `json:"hold_ms"`
	Fail      string `json:"fail"`       // "", retryable (the first fail_times attempts), permanent (the first)
	FailTimes int    `json:"fail_times"` // with fail retryable
	Rollback  bool   `json:"rollback"`   // the enqueuing transaction rolls back after the enqueue
}

// Jobs declares the relay, the probe jobs and, with PROBE_STEPS_EVERY, the steps singleton.
func (p *Probe) Jobs(j *work.Jobs) error {
	j.Add(work.Job{Name: "probe.relay", Kind: work.Queue, Effect: work.IdempotentExternal, Timeout: 10 * time.Second,
		Handle: p.relay})
	for effect, class := range map[string]work.Effect{"local": work.Local, "idempotent_external": work.IdempotentExternal,
		"non_idempotent_external": work.NonIdempotentExternal} {
		j.Add(work.Job{Name: jobNames[effect], Kind: work.Queue, Effect: class, MaxAttempts: JobAttempts, Handle: p.job,
			DeadLetter: p.deadLetter})
	}
	if every := p.rt.Config.Duration("PROBE_STEPS_EVERY"); every > 0 {
		j.Add(work.Job{Name: "probe.steps", Kind: work.Singleton, Effect: work.Local, Interval: every, Handle: p.steps})
	}
	return nil
}

func (p *Probe) relay(ctx context.Context, run *work.Run) error {
	var args struct{ Event string }
	if err := json.Unmarshal(run.Args, &args); err != nil {
		return work.Permanent(err)
	}
	if next := p.rt.Config.String("PROBE_NEXT"); next != "" {
		if err := p.echoNext(ctx, next, RelayBytes); err != nil {
			return err
		}
	}
	return run.Complete(ctx, func(ctx context.Context, tx db.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE probe_seen SET relayed_at = now() WHERE event_id = $1", args.Event)
		return err
	})
}

// enqueueJob writes a business row and enqueues a probe job in one transaction, `201 {"id": <item>}`.
func (p *Probe) enqueueJob(c *httpx.Context) error {
	var in jobIn
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	if in.Effect == "" {
		in.Effect = "local"
	}
	name, ok := jobNames[in.Effect]
	if !ok || in.HoldMS < 0 || in.HoldMS > MaxJobHoldMS || (in.Fail != "" && in.Fail != "retryable" &&
		in.Fail != "permanent") || in.FailTimes < 0 {
		return invalid("effect local, idempotent_external or non_idempotent_external; hold_ms 0..120000; fail empty, retryable or permanent")
	}
	var id string
	err := p.rt.DB.Tx(c.Ctx(), func(ctx context.Context, tx db.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO probe_job_requests (id) VALUES ($1)", ids.New().String()); err != nil {
			return err
		}
		var err error
		if id, err = p.rt.Work.Enqueue(ctx, tx, name, in); err != nil {
			return err
		}
		if in.Rollback {
			return errs.New(errs.ReasonRequestInvalid, "rolled back as asked (rollback)")
		}
		return nil
	})
	if err != nil {
		return err
	}
	return c.JSON(201, map[string]string{"id": id})
}

// job is one attempt of a probe job.
func (p *Probe) job(ctx context.Context, run *work.Run) error {
	var in jobIn
	if err := json.Unmarshal(run.Args, &in); err != nil {
		return work.Permanent(err)
	}
	attempt, err := p.attempt(ctx, run, 0)
	if err != nil {
		return err
	}
	ended := "failed"
	defer func() { p.ended(run.ID, attempt, 0, ended) }()
	switch {
	case in.Fail == "permanent":
		return work.Permanent(errors.New("failed as asked (permanent)"))
	case in.Fail == "retryable" && attempt <= in.FailTimes:
		return errors.New("failed as asked (retryable)")
	}
	if err := p.callFarSide(ctx, run, in.Effect); err != nil {
		return err
	}
	if attempt == 1 {
		if err := hold(ctx, in.HoldMS); err != nil {
			ended = "cancelled"
			return err
		}
	}
	if err := run.Complete(ctx, p.effect(run, 0)); err != nil {
		return err
	}
	ended = "done"
	return nil
}

// callFarSide is the external job's call, recorded on its far side: the idempotent one under the run's sub-key
// (P7.11), once per key; the non-idempotent one after marking the call as possibly sent (P7.12), every time.
func (p *Probe) callFarSide(ctx context.Context, run *work.Run, effect string) error {
	key, idempotent := run.SubKey("call"), true
	switch effect {
	case "local":
		return nil
	case "non_idempotent_external":
		if err := run.BeforeCall(ctx); err != nil {
			return err
		}
		key, idempotent = run.OperationID, false
	}
	return p.rt.DB.BackgroundTx(ctx, func(ctx context.Context, tx db.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO probe_job_calls (key, item, idempotent, instance) VALUES ($1, $2, $3, $4)
			ON CONFLICT (key) WHERE idempotent DO NOTHING`, key, run.ID, idempotent, p.instance)
		return err
	})
}

func (p *Probe) effect(run *work.Run, step int) func(ctx context.Context, tx db.Tx) error {
	return func(ctx context.Context, tx db.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO probe_job_effects (item, step, operation_id, instance) VALUES ($1, $2, $3, $4)",
			run.ID, step, run.OperationID, p.instance)
		return err
	}
}

func (p *Probe) deadLetter(ctx context.Context, tx db.Tx, item work.Item, cause error) error {
	_, err := tx.Exec(ctx, "INSERT INTO probe_job_dead (item, cause) VALUES ($1, $2)", item.ID, cause.Error())
	return err
}

// attempt records an attempt as it starts and returns its number for the item and step.
func (p *Probe) attempt(ctx context.Context, run *work.Run, step int) (int, error) {
	var n int
	err := p.rt.DB.BackgroundTx(ctx, func(ctx context.Context, tx db.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO probe_job_attempts (item, attempt, step, operation_id, instance)
			SELECT $1, count(*) + 1, $2, $3, $4 FROM probe_job_attempts WHERE item = $1 AND step = $2 RETURNING attempt`,
			run.ID, step, run.OperationID, p.instance).Scan(&n)
	})
	return n, err
}

// ended records how an attempt ended, after the run's context may have ended.
func (p *Probe) ended(item string, attempt, step int, how string) {
	_ = p.rt.DB.BackgroundTx(context.Background(), func(ctx context.Context, tx db.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE probe_job_attempts SET ended = $4, ended_at = now()
			WHERE item = $1 AND attempt = $2 AND step = $3`, item, attempt, step, how)
		return err
	})
}

func hold(ctx context.Context, ms int) error {
	select {
	case <-time.After(time.Duration(ms) * time.Millisecond):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type stepsIn struct {
	N        int `json:"n"`         // the steps of the next run, 1..20
	HoldStep int `json:"hold_step"` // the first process to reach this step waits there (up to 120 s); 0: none
}

// setSteps gives the steps singleton its next task, `202`.
func (p *Probe) setSteps(c *httpx.Context) error {
	var in stepsIn
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	if in.N < 1 || in.N > MaxSteps || in.HoldStep < 0 || in.HoldStep > in.N {
		return invalid("n 1..20, hold_step 0..n")
	}
	err := p.rt.DB.Tx(c.Ctx(), func(ctx context.Context, tx db.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO probe_steps (id, n, hold_step) VALUES (1, $1, $2)
			ON CONFLICT (id) DO UPDATE SET n = $1, hold_step = $2, held = false`, in.N, in.HoldStep)
		return err
	})
	if err != nil {
		return err
	}
	return c.JSON(202, in)
}

// steps runs the task in segments, one step a segment, each committed with its progress (P7.17); the last step
// completes the run and takes the task away.
func (p *Probe) steps(ctx context.Context, run *work.Run) error {
	var n, holdStep int
	var held bool
	err := p.rt.DB.BackgroundTx(ctx, func(ctx context.Context, tx db.Tx) error {
		return tx.QueryRow(ctx, "SELECT n, hold_step, held FROM probe_steps WHERE id = 1").Scan(&n, &holdStep, &held)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return run.Complete(ctx, nil)
	}
	if err != nil {
		return err
	}
	done := 0
	if run.Progress != nil {
		if err := json.Unmarshal(run.Progress, &done); err != nil {
			return work.Permanent(err)
		}
	}
	for s := done + 1; s <= n; s++ {
		attempt, err := p.attempt(ctx, run, s)
		if err != nil {
			return err
		}
		if s == holdStep && !held {
			if err := p.rt.DB.BackgroundTx(ctx, func(ctx context.Context, tx db.Tx) error {
				_, err := tx.Exec(ctx, "UPDATE probe_steps SET held = true WHERE id = 1")
				return err
			}); err != nil {
				return err
			}
			held = true
			if err := hold(ctx, MaxJobHoldMS); err != nil {
				p.ended(run.ID, attempt, s, "cancelled")
				return err
			}
		}
		if s == n {
			err = run.Complete(ctx, func(ctx context.Context, tx db.Tx) error {
				if err := p.effect(run, s)(ctx, tx); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, "DELETE FROM probe_steps WHERE id = 1")
				return err
			})
		} else {
			err = run.Checkpoint(ctx, s, p.effect(run, s))
		}
		if err != nil {
			return err
		}
		p.ended(run.ID, attempt, s, "done")
	}
	return nil
}
