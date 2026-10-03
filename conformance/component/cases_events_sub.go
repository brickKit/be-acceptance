package compconf

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/brickKit/be-acceptance/conformance/component/fakes"
	"github.com/nats-io/nats.go/jetstream"
)

// The events-sub profile (P12.5–P12.9, P12.14, P12.16, P11.8): the suite publishes as the
// producer and observes the component's own tables (observe.sql) and the dead letters.

func (r *Run) needSub(id string) []Consume {
	if r.natsConn == nil {
		r.ev.fail(id, "the component declares neither NATS_URL nor EVENT_BUS_URL (P12.12)")
		return nil
	}
	if !r.needMain(id) {
		return nil
	}
	cs := r.comp.Fixtures.Events.Consumes
	if len(cs) == 0 {
		r.ev.fail(id, "fixtures events.consumes is empty but events.subscribes is not (P12.16)")
	}
	return cs
}

// prepared runs a consumes entry's setup operation (when it has one) and returns its sample
// for a fresh aggregate (or the sample's own aggregate when setup needs it).
func (r *Run) prepared(ctx context.Context, c Consume) (*sample, error) {
	if c.Setup != "" {
		res, name := splitVia(c.Setup)
		op := r.comp.Fixtures.Resources[res][name]
		rid, _, err := r.create(ctx, res, "")
		if err != nil {
			return nil, err
		}
		if x := r.invoke(ctx, op, rid, "", nil); x.Status >= 300 {
			return nil, fmt.Errorf("setup %s = %d %s", c.Setup, x.Status, x.reason())
		}
		return r.newSample(c, true)
	}
	return r.newSample(c, false)
}

// dlqOf waits for dead letters of a durable carrying ce-id.
func (r *Run) dlqOf(durable, ceid string, d time.Duration) []busMsg {
	return r.bus.waitFor(func(m busMsg) bool {
		return strings.HasPrefix(m.Subject, "dlq."+durable+".") && (ceid == "" || m.Header.Get("ce-id") == ceid)
	}, 1, d)
}

// CP-EVS-01: each durable is named <id>__<subject>, with ack_wait 30 s, max_ack_pending 256,
// max_deliver −1, no backoff, deliver all; an event published while the component is not
// consuming is received once it is back (P12.5, P12.9).
func caseEVS01(ctx context.Context, r *Run) {
	const id = "CP-EVS-01"
	cs := r.needSub(id)
	js, err := r.js()
	if len(cs) == 0 || !r.ev.require(id, err == nil, "%v", err) {
		return
	}
	for _, c := range cs {
		name := durableName(r.comp.ID(), c.Subject)
		cons, err := js.Consumer(ctx, streamOf(c.Subject), name)
		if !r.ev.check(id, err == nil, "durable %s on %s: %v (P12.5)", name, streamOf(c.Subject), err) {
			continue
		}
		cfg := cons.CachedInfo().Config
		r.ev.check(id, cfg.AckWait == 30*time.Second && cfg.MaxAckPending == 256 && cfg.MaxDeliver == -1 && len(cfg.BackOff) == 0 &&
			cfg.DeliverPolicy == jetstream.DeliverAllPolicy && cfg.InactiveThreshold == 30*24*time.Hour,
			"durable %s: ack_wait %v, max_ack_pending %d, max_deliver %d, backoff %v, deliver %v, inactive %v (P12.5)",
			name, cfg.AckWait, cfg.MaxAckPending, cfg.MaxDeliver, cfg.BackOff, cfg.DeliverPolicy, cfg.InactiveThreshold)
	}
	c := cs[0]
	if c.Setup != "" && len(cs) > 1 {
		c = cs[1]
	}
	s, err := r.prepared(ctx, c)
	if !r.ev.require(id, err == nil, "preparing %s: %v", c.Subject, err) {
		return
	}
	if err := r.main.Pause(ctx); !r.ev.require(id, err == nil, "pausing the component: %v", err) {
		return
	}
	_, err = r.publishAs(ctx, s, 1, s.at(1, ""), nil)
	time.Sleep(time.Second)
	_ = r.main.Unpause(context.Background())
	if !r.ev.require(id, err == nil, "publishing: %v", err) {
		return
	}
	got := r.waitObserve(ctx, c.Observe.SQL, s.aggID, "", 30*time.Second)
	r.ev.check(id, got != "", "%s published while the component was paused was not applied within 30 s of it resuming (P12.5)", c.Subject)
}

// CP-EVS-02: a duplicate delivery (same ce-id and version, another broker message) takes
// effect once: the observed state and the cursor stay at that version (P12.6, P15.2).
func caseEVS02(ctx context.Context, r *Run) {
	const id = "CP-EVS-02"
	for _, c := range r.needSub(id) {
		if c.Setup != "" {
			continue
		}
		s, err := r.newSample(c, false)
		if !r.ev.require(id, err == nil, "%v", err) {
			return
		}
		ceid, err := r.publishAs(ctx, s, 2, s.at(2, s.variedField()), nil)
		if !r.ev.require(id, err == nil, "publishing: %v", err) {
			return
		}
		first := r.waitObserve(ctx, c.Observe.SQL, s.aggID, "", applyWait)
		if !r.ev.check(id, first != "", "%s not applied within 45 s", c.Subject) {
			return
		}
		_, err = r.publishAs(ctx, s, 2, s.at(2, s.variedField()), map[string]string{"Nats-Msg-Id": "redelivery-" + ceid, "ce-id": ceid})
		r.ev.require(id, err == nil, "publishing the duplicate: %v", err)
		time.Sleep(3 * time.Second)
		after, _ := r.observe(ctx, c.Observe.SQL, s.aggID)
		r.ev.check(id, after == first, "after a duplicate the state is %q, was %q (P12.6)", after, first)
		r.checkCursor(ctx, id, c, s, 2)
		return
	}
	r.ev.notApplicable(id, "every consumed subject needs a setup operation")
}

// checkCursor: besdk_event_cursor holds the aggregate at version v (P12.6).
func (r *Run) checkCursor(ctx context.Context, id string, c Consume, s *sample, v int) {
	var got int64
	err := r.pgScan(ctx, `SELECT version FROM besdk_event_cursor WHERE consumer = $1 AND aggregate_type = $2 AND aggregate_id = $3`,
		[]any{c.Consumer, s.def.AggregateType, s.aggID}, &got)
	r.ev.check(id, err == nil && got == int64(v), "besdk_event_cursor for %s %s = %d (%v), want %d (P12.6)", s.def.AggregateType, s.aggID, got, err, v)
}

func (r *Run) pgScan(ctx context.Context, sql string, args []any, dst ...any) error {
	conn, err := r.dbConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	return conn.QueryRow(ctx, sql, args...).Scan(dst...)
}

// CP-EVS-03: versions delivered shuffled and duplicated end in the same state as in order
// (property test over a few orders) (P12.6, P15.1).
func caseEVS03(ctx context.Context, r *Run) {
	const id = "CP-EVS-03"
	for _, c := range r.needSub(id) {
		if c.Setup != "" {
			continue
		}
		orders := [][]int{{1, 2, 3}, {3, 2, 1}, {2, 3, 1, 3}, {3, 1, 1, 2}}
		aggs := make([]string, len(orders))
		for i, order := range orders {
			s, err := r.newSample(c, false)
			if !r.ev.require(id, err == nil, "%v", err) {
				return
			}
			aggs[i] = s.aggID
			field := s.variedField()
			for _, v := range order {
				if _, err := r.publishAs(ctx, s, v, s.at(v, field), nil); err != nil {
					r.ev.fail(id, "publishing: %v", err)
					return
				}
			}
		}
		// The states are read once every message has been handled, which the durable itself
		// says. Reading the in-order aggregate as soon as it shows any state took version 2 as
		// the reference in slower runs, and every other order then "differed" from it.
		if !r.ev.require(id, r.waitDrained(ctx, c.Subject, applyWait),
			"the durable of %s still has undelivered or unacknowledged messages after %v", c.Subject, applyWait) {
			return
		}
		states := make([]string, len(orders))
		for i, agg := range aggs {
			o, _ := r.observe(ctx, c.Observe.SQL, agg)
			states[i] = normalise(o, agg)
		}
		r.ev.check(id, states[0] != "", "%s in order was not applied", c.Subject)
		for i := 1; i < len(orders); i++ {
			r.ev.check(id, states[i] == states[0], "versions in order %v end in %q, in order 1,2,3 in %q (P12.6)", orders[i], states[i], states[0])
		}
		return
	}
	r.ev.notApplicable(id, "every consumed subject needs a setup operation")
}

// normalise hides the aggregate ID in an observed row so rows of two aggregates compare.
func normalise(row, agg string) string { return strings.ReplaceAll(row, agg, "<agg>") }

// CP-EVS-04: a message without ce-id, and one whose payload is not JSON, go to the dead letters
// at once with be-dlq-reason, be-dlq-consumer and be-dlq-delivery (P12.7, P12.14).
func caseEVS04(ctx context.Context, r *Run) {
	const id = "CP-EVS-04"
	for _, c := range r.needSub(id) {
		s, err := r.newSample(c, false)
		if !r.ev.require(id, err == nil, "%v", err) {
			return
		}
		durable := durableName(r.comp.ID(), c.Subject)
		noID, err := r.publishAs(ctx, s, 1, s.at(1, ""), map[string]string{"ce-id": "", "Nats-Msg-Id": "cc-noid-" + uuidv7()})
		r.ev.require(id, err == nil, "publishing: %v", err)
		got := r.bus.waitFor(func(m busMsg) bool {
			return strings.HasPrefix(m.Subject, "dlq."+durable+".") && m.Header.Get("ce-subject") == s.aggID
		}, 1, applyWait)
		r.ev.check(id, len(got) == 1, "a %s without ce-id: %d dead letters within 45 s, want 1 (P12.14) [%s]", c.Subject, len(got), noID)
		r.checkDLQHeaders(id, got, durable, c.Subject)
		bad, err := r.publishAs(ctx, s, 1, []byte("not json {"), nil)
		r.ev.require(id, err == nil, "publishing: %v", err)
		got = r.dlqOf(durable, bad, applyWait)
		r.ev.check(id, len(got) == 1, "a %s with an unparsable payload: %d dead letters within 45 s, want 1 (P12.7)", c.Subject, len(got))
		r.checkDLQHeaders(id, got, durable, c.Subject)
	}
}

func (r *Run) checkDLQHeaders(id string, got []busMsg, durable, subject string) {
	for _, m := range got {
		r.ev.check(id, m.Subject == "dlq."+durable+"."+subject, "dead letter on %s, want dlq.%s.%s (P12.7)", m.Subject, durable, subject)
		r.ev.check(id, m.Header.Get("be-dlq-reason") != "" && m.Header.Get("be-dlq-consumer") == durable && m.Header.Get("be-dlq-delivery") != "",
			"dead letter headers reason %q consumer %q delivery %q (P12.7)", m.Header.Get("be-dlq-reason"), m.Header.Get("be-dlq-consumer"), m.Header.Get("be-dlq-delivery"))
		r.ev.check(id, strings.HasPrefix(m.Header.Get("Nats-Msg-Id"), "dlq:"+durable+":"), "dead letter message ID %q, want dlq:%s:<seq> (P12.7)", m.Header.Get("Nats-Msg-Id"), durable)
	}
}

// revoke takes the runtime role's table grants away (a temporary failure) and returns the
// function that gives them back.
func (r *Run) revoke(ctx context.Context) (func(), error) {
	revoke := fmt.Sprintf("REVOKE SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %s FROM %s", qi(r.db.Schema), qi(r.db.Runtime))
	grant := fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %s TO %s", qi(r.db.Schema), qi(r.db.Runtime))
	if err := r.superExec(ctx, r.db.Database, revoke); err != nil {
		return nil, err
	}
	return func() { _ = r.superExec(context.Background(), r.db.Database, grant) }, nil
}

// soloInstance stops the main instance and starts another with set overriding the environment;
// the returned function removes it and starts a fresh main instance.
//
// Main is stopped, not paused. A paused pull consumer leaves its last pull request alive at the
// server, so a message published right after goes to the frozen process and waits out the ack
// wait (30 s) there: CP-EVS-05 and CP-EVS-08 then saw nothing within their 15 s. A component
// that stops closes its connection, and the server drops its pull requests at once.
func (r *Run) soloInstance(ctx context.Context, name string, set map[string]string) (*instance, func(), error) {
	if err := r.main.Stop(ctx, r.comp.Manifest.Deployment.StopGracePeriodSeconds); err != nil {
		return nil, nil, err
	}
	_ = r.main.Remove(ctx)
	if r.stopLogs != nil {
		r.stopLogs()
	}
	r.mainUp = false
	r.waitPullsGone(ctx, 35*time.Second)
	in, err := r.startInstance(ctx, name, envWith(r.compEnv, set), fakes.NewLogs())
	if err == nil {
		_, err = waitStatus(ctx, in.base+"/readyz", 200, 60*time.Second)
	}
	done := func() {
		bg := context.Background()
		if in != nil {
			_ = in.c.Remove(bg)
			in.stop()
			r.waitPullsGone(bg, 35*time.Second)
		}
		if err := r.startMain(bg); err != nil {
			r.logf("restarting the main instance after %s: %v", name, err)
			return
		}
		if _, err := waitStatus(bg, r.base+"/readyz", 200, 60*time.Second); err != nil {
			r.logf("the main instance restarted after %s is not ready: %v", name, err)
			return
		}
		r.mainUp = true
	}
	if err != nil {
		done()
		return nil, nil, err
	}
	return in, done, nil
}

// applyWait bounds how long a case waits for a published message to be handled (applied, dead
// lettered, or answered by an event of the handler). It is the durable's ack wait (30 s, P12.5)
// plus a margin. Whenever an instance of the component has just stopped or was paused, one
// message may be handed to a pull request nobody reads and is redelivered only after the ack
// wait; with 15 s the reference sequence of CP-EVS-03 stopped at version 2 in a full run. The
// waits return as soon as the effect is seen, so a healthy run is not slower.
const applyWait = 45 * time.Second

// waitDrained waits until the component's durable for subject has nothing left to deliver and
// nothing unacknowledged, or limit passes; it reports whether the durable drained. A message
// negatively acknowledged with a delay counts as unacknowledged until it is handled.
func (r *Run) waitDrained(ctx context.Context, subject string, limit time.Duration) bool {
	js, err := r.js()
	if err != nil {
		return false
	}
	for end := time.Now().Add(limit); ; time.Sleep(200 * time.Millisecond) {
		if c, err := js.Consumer(ctx, streamOf(subject), durableName(r.comp.ID(), subject)); err == nil {
			if info, err := c.Info(ctx); err == nil && info.NumPending == 0 && info.NumAckPending == 0 {
				return true
			}
		}
		if time.Now().After(end) || ctx.Err() != nil {
			return false
		}
	}
}

// waitPullsGone waits until no pull request is waiting on the component's durables, or limit
// passes. The last pull request of an instance that stopped stays registered at the server
// until it expires (how long is the component's choice); a message published before that is
// handed to the request nobody reads and then waits out the ack wait. The suite therefore asks
// the server instead of guessing a delay.
func (r *Run) waitPullsGone(ctx context.Context, limit time.Duration) {
	js, err := r.js()
	if err != nil {
		return
	}
	end := time.Now().Add(limit)
	for _, subject := range r.comp.Manifest.Events.Subscribes {
		if strings.HasSuffix(subject, "*") {
			continue
		}
		for time.Now().Before(end) && ctx.Err() == nil {
			c, err := js.Consumer(ctx, streamOf(subject), durableName(r.comp.ID(), subject))
			if err != nil {
				break
			}
			info, err := c.Info(ctx)
			if err != nil || info.NumWaiting == 0 {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
}

// tailLogs writes the last n lines an instance logged to the progress output, so that a failed
// case shows what the component said; the instance is removed when the case ends.
func (r *Run) tailLogs(id string, in *instance, n int) {
	if in == nil || in.logs == nil {
		return
	}
	lines := in.logs.Lines()
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("\n    " + l.Raw)
	}
	r.logf("%s failed; the last %d log lines of its instance:%s", id, len(lines), b.String())
}

// CP-EVS-05: a handler failing for a while (the suite revokes the table grants) is negatively
// acknowledged and redelivered with EVENTS_BACKOFF; it takes effect once the cause is gone,
// without a dead letter (P12.7).
func caseEVS05(ctx context.Context, r *Run) {
	const id = "CP-EVS-05"
	cs := r.needSub(id)
	var c *Consume
	for i := range cs {
		if cs[i].Setup == "" && c == nil {
			c = &cs[i]
		}
	}
	if c == nil {
		r.ev.notApplicable(id, "every consumed subject needs a setup operation")
		return
	}
	in, done, err := r.soloInstance(ctx, "evs05", map[string]string{"EVENTS_MAX_DELIVER": "20", "EVENTS_BACKOFF": "1s"})
	if !r.ev.require(id, err == nil, "a dedicated instance: %v", err) {
		return
	}
	defer done()
	s, err := r.newSample(*c, false)
	if !r.ev.require(id, err == nil, "%v", err) {
		return
	}
	regrant, err := r.revoke(ctx)
	if !r.ev.require(id, err == nil, "revoking: %v", err) {
		return
	}
	ceid, err := r.publishAs(ctx, s, 1, s.at(1, ""), nil)
	time.Sleep(3500 * time.Millisecond)
	regrant()
	if !r.ev.require(id, err == nil, "publishing: %v", err) {
		return
	}
	got := r.waitObserve(ctx, c.Observe.SQL, s.aggID, "", applyWait)
	if got == "" {
		r.tailLogs(id, in, 25)
	}
	r.ev.check(id, got != "", "%s not applied within 45 s after the table grants came back (P12.7: nak with delay, redeliver)", c.Subject)
	r.ev.check(id, len(r.dlqOf(durableName(r.comp.ID(), c.Subject), ceid, time.Second)) == 0, "a temporary failure was dead-lettered (P12.7)")
}

// CP-EVS-06: an event published by a handler carries ce-causationid = the handled ce-id and
// ce-hopcount + 1; an inbound event with hop count 11 goes to the dead letters (P12.8).
func caseEVS06(ctx context.Context, r *Run) {
	const id = "CP-EVS-06"
	n := 0
	for _, c := range r.needSub(id) {
		durable := durableName(r.comp.ID(), c.Subject)
		s, err := r.prepared(ctx, c)
		if !r.ev.require(id, err == nil, "preparing %s: %v", c.Subject, err) {
			continue
		}
		hot, err := r.publishAs(ctx, s, 50, s.at(50, ""), map[string]string{"ce-hopcount": "11"})
		r.ev.require(id, err == nil, "publishing: %v", err)
		r.ev.check(id, len(r.dlqOf(durable, hot, applyWait)) == 1, "a %s with ce-hopcount 11 was not dead-lettered within 45 s (P12.8)", c.Subject)
		if c.Produces == "" {
			continue
		}
		n++
		ceid, err := r.publishAs(ctx, s, 60, s.at(60, ""), map[string]string{"ce-hopcount": "3"})
		r.ev.require(id, err == nil, "publishing: %v", err)
		got := r.bus.waitFor(func(m busMsg) bool { return m.Subject == c.Produces && m.Header.Get("ce-causationid") == ceid }, 1, applyWait)
		if r.ev.check(id, len(got) == 1, "handling %s did not publish %s with ce-causationid %s within 45 s (P12.8)", c.Subject, c.Produces, ceid) {
			r.ev.check(id, got[0].Header.Get("ce-hopcount") == "4", "%s caused by hop count 3 has ce-hopcount %q, want 4 (P12.8)", c.Produces, got[0].Header.Get("ce-hopcount"))
		}
	}
	if n == 0 {
		r.ev.pass(id, "no consumed subject declares produces: only the hop-count limit checked")
	}
}

// CP-EVS-07: a transaction-document event without its legal entity goes to the dead letters
// (P11.8).
func caseEVS07(ctx context.Context, r *Run) {
	const id = "CP-EVS-07"
	n := 0
	for _, c := range r.needSub(id) {
		s, err := r.newSample(c, false)
		if err != nil || !s.def.TxDocument {
			continue
		}
		n++
		delete(s.payload, "legal_entity_id")
		ceid, err := r.publishAs(ctx, s, 1, s.at(1, ""), map[string]string{"ce-legalentity": ""})
		r.ev.require(id, err == nil, "publishing: %v", err)
		r.ev.check(id, len(r.dlqOf(durableName(r.comp.ID(), c.Subject), ceid, applyWait)) == 1,
			"a %s (transaction document) without legal entity was not dead-lettered within 45 s (P11.8)", c.Subject)
	}
	if n == 0 {
		r.ev.notApplicable(id, "no consumed subject is a transaction document in its producer's contract")
	}
}

// CP-EVS-08: with EVENTS_MAX_DELIVER=2 a message that always fails is dead-lettered once (ID
// dlq:<durable>:<stream seq>) and terminated; the durable keeps max_deliver −1, no backoff,
// ack_wait 30 s; a change the operator made to an existing durable survives a start (P12.5,
// P12.7).
func caseEVS08(ctx context.Context, r *Run) {
	const id = "CP-EVS-08"
	cs := r.needSub(id)
	js, err := r.js()
	if len(cs) == 0 || !r.ev.require(id, err == nil, "%v", err) {
		return
	}
	c := cs[0]
	durable := durableName(r.comp.ID(), c.Subject)
	cons, err := js.Consumer(ctx, streamOf(c.Subject), durable)
	if !r.ev.require(id, err == nil, "durable %s: %v", durable, err) {
		return
	}
	cfg := cons.CachedInfo().Config
	cfg.MaxAckPending = 100
	_, err = js.UpdateConsumer(ctx, streamOf(c.Subject), cfg)
	r.ev.require(id, err == nil, "changing the durable: %v", err)
	in, done, err := r.soloInstance(ctx, "evs08", map[string]string{"EVENTS_MAX_DELIVER": "2", "EVENTS_BACKOFF": "200ms"})
	if !r.ev.require(id, err == nil, "a dedicated instance: %v", err) {
		return
	}
	defer done()
	s, err := r.newSample(c, c.Setup != "")
	if !r.ev.require(id, err == nil, "%v", err) {
		return
	}
	regrant, err := r.revoke(ctx)
	if !r.ev.require(id, err == nil, "revoking: %v", err) {
		return
	}
	ceid, err := r.publishAs(ctx, s, 70, s.at(70, ""), nil)
	r.dlqOf(durable, ceid, applyWait)
	time.Sleep(3 * time.Second)
	got := r.bus.find(func(m busMsg) bool {
		return strings.HasPrefix(m.Subject, "dlq."+durable+".") && m.Header.Get("ce-id") == ceid
	})
	regrant()
	r.ev.require(id, err == nil, "publishing: %v", err)
	if len(got) != 1 {
		r.tailLogs(id, in, 25)
	}
	if r.ev.check(id, len(got) == 1, "a message failing with EVENTS_MAX_DELIVER=2: %d dead letters, want exactly 1 (P12.7)", len(got)) {
		r.ev.check(id, got[0].Header.Get("be-dlq-delivery") == "3", "be-dlq-delivery %q, want 3: dead-lettered on the receipt after the second failed delivery (P12.7)", got[0].Header.Get("be-dlq-delivery"))
		r.ev.check(id, regexpDLQ.MatchString(got[0].Header.Get("Nats-Msg-Id")), "dead letter ID %q, want dlq:<durable>:<stream seq>", got[0].Header.Get("Nats-Msg-Id"))
	}
	cons, err = js.Consumer(ctx, streamOf(c.Subject), durable)
	if r.ev.check(id, err == nil, "durable %s: %v", durable, err) {
		info := cons.CachedInfo()
		cfg := info.Config
		r.ev.check(id, cfg.MaxAckPending == 100, "the durable's max_ack_pending is %d after a start; the operator set 100 (P12.5: never updated)", cfg.MaxAckPending)
		r.ev.check(id, cfg.MaxDeliver == -1 && len(cfg.BackOff) == 0 && cfg.AckWait == 30*time.Second, "durable max_deliver %d backoff %v ack_wait %v (P12.5)", cfg.MaxDeliver, cfg.BackOff, cfg.AckWait)
		r.ev.check(id, info.NumAckPending == 0, "%d messages still pending on %s: the dead-lettered one must be terminated (P12.7)", info.NumAckPending, durable)
	}
}

var regexpDLQ = regexp.MustCompile(`^dlq:[a-z0-9_-]+__[a-z0-9_]+:[0-9]+$`)

// CP-EVS-09: the durables of the component on every stream are exactly those of
// events.subscribes (P12.16, P12.5).
func caseEVS09(ctx context.Context, r *Run) {
	const id = "CP-EVS-09"
	if r.natsConn == nil || !r.needMain(id) {
		return
	}
	js, err := r.js()
	if !r.ev.require(id, err == nil, "%v", err) {
		return
	}
	var want, got []string
	for _, s := range r.comp.Manifest.Events.Subscribes {
		want = append(want, durableName(r.comp.ID(), s))
	}
	prefix := strings.ReplaceAll(r.comp.ID(), "/", "_") + "__"
	for name := range js.StreamNames(ctx).Name() {
		st, err := js.Stream(ctx, name)
		if err != nil {
			continue
		}
		for c := range st.ConsumerNames(ctx).Name() {
			if strings.HasPrefix(c, prefix) {
				got = append(got, c)
			}
		}
	}
	sort.Strings(want)
	sort.Strings(got)
	r.ev.check(id, strings.Join(want, ",") == strings.Join(got, ","), "durables of the component %v, events.subscribes gives %v (P12.16)", got, want)
	fixtures := []string{}
	for _, c := range r.comp.Fixtures.Events.Consumes {
		fixtures = append(fixtures, c.Subject)
	}
	subs := append([]string(nil), r.comp.Manifest.Events.Subscribes...)
	sort.Strings(fixtures)
	sort.Strings(subs)
	r.ev.check(id, strings.Join(fixtures, ",") == strings.Join(subs, ","), "fixtures events.consumes %v, events.subscribes %v (P12.16)", fixtures, subs)
}
