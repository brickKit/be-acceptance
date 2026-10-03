package main

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// The consumer of conformance.owner.updated.v1 (P12.5–P12.9, P12.14): an Apply handler that
// keeps owner_snapshots at the owner's newest version through the aggregate-stream cursor, and
// publishes conformance.rawstub.owner_noted.v1 in the same transaction.

// permanent marks an error that dead-letters at once.
type permanent struct{ reason string }

func (p permanent) Error() string { return p.reason }

func (a *App) runConsumer(ctx context.Context) {
	supervise(ctx, a.log, "consumer "+consumedSubject, func(ctx context.Context) {
		cons, err := a.bus.js.Consumer(ctx, streamName(consumedSubject), durable(consumedSubject))
		if err != nil {
			a.log.Warn("consumer_unavailable", F{"error": err.Error()})
			_ = a.bus.ensure(ctx)
			return // supervise restarts with backoff
		}
		for ctx.Err() == nil {
			batch, err := cons.Fetch(4, jetstream.FetchMaxWait(2*time.Second))
			if err != nil {
				if !sleepCtx(ctx, time.Second) {
					return
				}
				continue
			}
			for m := range batch.Messages() {
				a.handle(ctx, m)
			}
		}
	})
}

// handle applies P12.7: over max deliveries or a permanent error → dead letter; another error
// → nak with the backoff of the delivery count.
func (a *App) handle(ctx context.Context, m jetstream.Msg) {
	md, err := m.Metadata()
	if err != nil {
		_ = m.Nak()
		return
	}
	n := int(md.NumDelivered)
	if n > a.cfg.EventsMaxDeliver {
		a.deadLetter(ctx, m, md, "max_deliver")
		return
	}
	err = a.apply(ctx, m)
	var p permanent
	switch {
	case err == nil:
		_ = m.Ack()
	case errors.As(err, &p):
		a.deadLetter(ctx, m, md, p.reason)
	default:
		b := a.cfg.EventsBackoff
		delay := b[min(n-1, len(b)-1)]
		a.log.Warn("event_handler_failed", F{"subject": m.Subject(), "delivery": n, "error": rootCause(err).Error()})
		_ = m.NakWithDelay(delay)
	}
}

// apply checks the envelope, then advances the cursor and applies in one transaction.
func (a *App) apply(ctx context.Context, m jetstream.Msg) error {
	h := m.Headers()
	ceid := h.Get("ce-id")
	if ceid == "" {
		return permanent{"missing ce-id"} // P12.14
	}
	hop, err := strconv.Atoi(h.Get("ce-hopcount"))
	if err != nil || hop > 10 {
		return permanent{"hop count above 10"} // P12.8
	}
	version, err := strconv.ParseInt(h.Get("ce-aggregateversion"), 10, 64)
	if err != nil || version < 1 {
		return permanent{"bad ce-aggregateversion"}
	}
	var p struct {
		OwnerID     string `json:"owner_id"`
		DisplayName string `json:"display_name"`
		CreditLimit string `json:"credit_limit"`
		Currency    string `json:"currency"`
	}
	if json.Unmarshal(m.Data(), &p) != nil || p.OwnerID == "" {
		return permanent{"payload does not match the contract"}
	}
	aggType := h.Get("ce-aggregatetype")
	if aggType == "" {
		aggType = consumedAggType
	}
	hctx, cancel := context.WithTimeout(ctx, 25*time.Second) // ack_wait − 5 s (P12.9)
	defer cancel()
	return a.db.Tx(hctx, func(tx pgx.Tx) error {
		var one int
		err := tx.QueryRow(hctx, a.db.Q(`INSERT INTO besdk_event_cursor (consumer, aggregate_type, aggregate_id, version, event_id)
			VALUES ('', $1, $2, $3, $4) ON CONFLICT (consumer, aggregate_type, aggregate_id) DO UPDATE
			SET version = EXCLUDED.version, event_id = EXCLUDED.event_id, seen_at = now()
			WHERE besdk_event_cursor.version < EXCLUDED.version RETURNING 1`), aggType, h.Get("ce-subject"), version, ceid).Scan(&one)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // a duplicate or an older version (P12.6)
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(hctx, a.db.Q(`INSERT INTO owner_snapshots (owner_id, display_name, credit_limit, currency, version)
			VALUES ($1, $2, $3, $4, $5) ON CONFLICT (owner_id) DO UPDATE SET display_name = EXCLUDED.display_name,
			credit_limit = EXCLUDED.credit_limit, currency = EXCLUDED.currency, version = EXCLUDED.version`),
			p.OwnerID, p.DisplayName, p.CreditLimit, p.Currency, version); err != nil {
			return err
		}
		return a.enqueue(hctx, tx, event{subject: "conformance.rawstub.owner_noted.v1", aggID: p.OwnerID, version: version,
			payload: map[string]any{"owner_id": p.OwnerID, "version": version}, traceparent: h.Get("traceparent"), causation: ceid, hop: hop + 1})
	})
}

// deadLetter publishes to dlq.<durable>.<subject> with the ID dlq:<durable>:<stream seq>, the
// original ce-* headers and be-dlq-*, then terminates the original (P12.7).
func (a *App) deadLetter(ctx context.Context, m jetstream.Msg, md *jetstream.MsgMetadata, reason string) {
	d := durable(consumedSubject)
	out := nats.NewMsg("dlq." + d + "." + m.Subject())
	out.Data = m.Data()
	for k, v := range m.Headers() {
		if len(k) > 3 && (k[:3] == "ce-" || k[:3] == "Ce-") || k == "traceparent" {
			out.Header[k] = v
		}
	}
	out.Header.Set("be-dlq-reason", reason)
	out.Header.Set("be-dlq-consumer", d)
	out.Header.Set("be-dlq-delivery", strconv.FormatUint(md.NumDelivered, 10))
	out.Header.Set("Nats-Msg-Id", "dlq:"+d+":"+strconv.FormatUint(md.Sequence.Stream, 10))
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := a.bus.js.PublishMsg(pctx, out); err != nil {
		a.log.Warn("dead_letter_failed", F{"error": err.Error()})
		_ = m.Nak()
		return
	}
	a.log.Warn("event_dead_lettered", F{"subject": m.Subject(), "reason": reason, "delivery": md.NumDelivered})
	_ = m.Term()
}
