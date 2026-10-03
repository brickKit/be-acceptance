package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
)

// The outbox (P12.1, P12.2, P12.8): an event is a besdk_outbox row written in the business
// transaction; the pump publishes it after commit and marks it PUBLISHED only after the PubAck.

const maxPayload = 64 << 10

// event is one row to write.
type event struct {
	subject, aggType, aggID string
	version                 int64
	payload                 any
	traceparent, causation  string
	hop                     int
}

var aggTypes = map[string]string{
	"conformance.rawstub.created.v1":     "conformance.rawstub.note",
	"conformance.rawstub.archived.v1":    "conformance.rawstub.note",
	"conformance.rawstub.owner_noted.v1": "conformance.rawstub.owner_note",
}

// enqueue writes the row in tx. A payload above 64 KiB fails the transaction as a programming
// error (P12.2: INTERNAL, naming the subject and the size).
func (a *App) enqueue(ctx context.Context, tx pgx.Tx, e event) error {
	payload, err := json.Marshal(e.payload)
	if err != nil {
		return err
	}
	if len(payload) > maxPayload {
		return internalErr(fmt.Errorf("event %s payload of %d bytes is above 64 KiB", e.subject, len(payload)))
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	_, err = tx.Exec(ctx, a.db.Q(`INSERT INTO besdk_outbox (id, created_at, subject, aggregate_type, aggregate_id, aggregate_version,
		occurred_at, traceparent, causation_id, hop_count, payload) VALUES ($1, $2, $3, $4, $5, $6, $2, $7, $8, $9, $10)`),
		newUUIDv7(now), now, e.subject, aggTypes[e.subject], e.aggID, e.version, e.traceparent, e.causation, e.hop, payload)
	return err
}

// outRow is a claimed row.
type outRow struct {
	id                      string
	createdAt, occurredAt   time.Time
	subject, aggType, aggID string
	version                 int64
	traceparent, causation  string
	hop, attempts           int
	payload                 []byte
}

// claimSQL is the reference claim (ddl/02-outbox.sql, P10.9).
const claimSQL = `UPDATE besdk_outbox SET status = 'SENDING', claimed_until = now() + interval '30 seconds', attempts = attempts + 1
	WHERE (id, created_at) IN (SELECT id, created_at FROM besdk_outbox
		WHERE (status = 'PENDING' AND next_attempt_at <= now()) OR (status = 'SENDING' AND claimed_until < now())
		ORDER BY created_at, id LIMIT 256 FOR UPDATE SKIP LOCKED)
	RETURNING id::text, created_at, occurred_at, subject, aggregate_type, aggregate_id, aggregate_version, traceparent,
		causation_id, hop_count, attempts, payload`

// selectClaimSQL is the broken variant select-claim: read, then update; not a claim.
const selectClaimSQL = `SELECT id::text, created_at, occurred_at, subject, aggregate_type, aggregate_id, aggregate_version, traceparent,
		causation_id, hop_count, attempts, payload FROM besdk_outbox WHERE status <> 'PUBLISHED' AND next_attempt_at <= now()
		ORDER BY created_at, id LIMIT 256`

func (a *App) claim(ctx context.Context) ([]outRow, error) {
	var out []outRow
	err := a.db.Tx(ctx, func(tx pgx.Tx) error {
		q := claimSQL
		if broken == "select-claim" {
			q = selectClaimSQL
		}
		rows, err := tx.Query(ctx, a.db.Q(q))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r outRow
			if err := rows.Scan(&r.id, &r.createdAt, &r.occurredAt, &r.subject, &r.aggType, &r.aggID, &r.version, &r.traceparent,
				&r.causation, &r.hop, &r.attempts, &r.payload); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if broken == "select-claim" && len(out) > 0 {
		time.Sleep(100 * time.Millisecond) // widens the window two replicas both read the same rows
	}
	return out, err
}

// envelope is the CloudEvents binary-mode message of a row (P12, envelope).
func envelope(r outRow) *nats.Msg {
	m := nats.NewMsg(r.subject)
	m.Data = r.payload
	h := map[string]string{
		"ce-specversion": "1.0", "ce-id": r.id, "ce-source": componentID, "ce-type": r.subject,
		"ce-time": r.occurredAt.UTC().Format(time.RFC3339Nano), "ce-subject": r.aggID, "content-type": "application/json",
		"ce-dataschema":    componentID + "@" + componentVersion + "/contracts/events/rawstub.events.json#" + r.subject,
		"ce-aggregatetype": r.aggType, "ce-aggregateversion": strconv.FormatInt(r.version, 10), "ce-hopcount": strconv.Itoa(r.hop),
		"ce-causationid": r.causation, "traceparent": r.traceparent, "Nats-Msg-Id": r.id,
	}
	if broken == "no-ce-id" {
		delete(h, "ce-id")
		delete(h, "Nats-Msg-Id")
	}
	for k, v := range h {
		if v != "" {
			m.Header.Set(k, v)
		}
	}
	return m
}

// runPump publishes claimed rows: every 200 ms while busy, every 2 s while idle.
func (a *App) runPump(ctx context.Context) {
	supervise(ctx, a.log, "be.outbox", func(ctx context.Context) {
		for {
			rows, err := a.claim(ctx)
			if err != nil {
				a.log.Warn("outbox_claim_failed", F{"error": rootCause(err).Error()})
			}
			for _, r := range rows {
				a.publishRow(ctx, r)
			}
			wait := 2 * time.Second
			if len(rows) > 0 {
				wait = 200 * time.Millisecond
			}
			if !sleepCtx(ctx, wait) {
				return
			}
		}
	})
}

func (a *App) publishRow(ctx context.Context, r outRow) {
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	_, pubErr := a.bus.js.PublishMsg(pctx, envelope(r))
	cancel()
	err := a.db.Tx(ctx, func(tx pgx.Tx) error {
		if pubErr == nil {
			_, e := tx.Exec(ctx, a.db.Q(`UPDATE besdk_outbox SET status = 'PUBLISHED', published_at = now(), claimed_until = NULL
				WHERE id = $1 AND created_at = $2`), r.id, r.createdAt)
			return e
		}
		backoff := min(time.Duration(1<<min(r.attempts-1, 6))*time.Second, time.Minute)
		_, e := tx.Exec(ctx, a.db.Q(`UPDATE besdk_outbox SET status = 'PENDING', next_attempt_at = now() + $3::interval,
			claimed_until = NULL, last_error = $4 WHERE id = $1 AND created_at = $2`), r.id, r.createdAt, backoff.String(), "publish failed")
		return e
	})
	if err != nil {
		a.log.Warn("outbox_mark_failed", F{"error": rootCause(err).Error()})
	}
}
