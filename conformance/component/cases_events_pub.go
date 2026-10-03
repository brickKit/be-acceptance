package compconf

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/brickKit/be-acceptance/conformance/component/protoschema"
	"github.com/nats-io/nats.go/jetstream"
)

// The events-pub profile (P12.1–P12.4, P12.16): the component publishes through its outbox.

// produced invokes a produces entry and returns the aggregate ID its event is about.
func (r *Run) produce(ctx context.Context, p Produce, opts ...reqOpt) (string, *exchange, error) {
	res, name := splitVia(p.Via)
	op, ok := r.comp.Fixtures.Resources[res][name]
	if !ok {
		return "", nil, fmt.Errorf("produces via %s is not a fixtures operation", p.Via)
	}
	if name == "create" {
		id, x, err := r.create(ctx, res, "", opts...)
		return id, x, err
	}
	id, _, err := r.create(ctx, res, "")
	if err != nil {
		return "", nil, err
	}
	x := r.invoke(ctx, op, id, "", nil, opts...)
	if x.Status >= 300 {
		return id, x, fmt.Errorf("%s = %d %s", p.Via, x.Status, x.reason())
	}
	return id, x, nil
}

func eventOf(subject, aggID string) func(busMsg) bool {
	return func(m busMsg) bool { return m.Subject == subject && m.Header.Get("ce-subject") == aggID }
}

func (r *Run) needPub(id string) []Produce {
	if r.natsConn == nil {
		r.ev.fail(id, "the component declares neither NATS_URL nor EVENT_BUS_URL (P12.12)")
		return nil
	}
	if !r.needMain(id) {
		return nil
	}
	ps := r.comp.Fixtures.Events.Produces
	if len(ps) == 0 {
		r.ev.fail(id, "fixtures events.produces names no operation that publishes (P12.16)")
	}
	return ps
}

var rfc3339 = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z$`)

// CP-EVP-01: each produces operation puts its event on the bus with the complete envelope:
// ce-id a UUIDv7 equal to Nats-Msg-Id, the contract's aggregate type, hop count 0 and no
// causation from a request, and the request's trace (P12.1, P12.3).
func caseEVP01(ctx context.Context, r *Run) {
	const id = "CP-EVP-01"
	own := r.ownEvents()
	for _, p := range r.needPub(id) {
		tid := strings.ReplaceAll(uuidv7(), "-", "")
		agg, _, err := r.produce(ctx, p, withHeader("traceparent", "00-"+tid+"-"+tid[:16]+"-01"))
		if !r.ev.require(id, err == nil, "%v", err) {
			continue
		}
		got := r.bus.waitFor(eventOf(p.Subject, agg), 1, 10*time.Second)
		if !r.ev.check(id, len(got) > 0, "no %s about %s within 10 s of %s (P12.1)", p.Subject, agg, p.Via) {
			continue
		}
		h := got[0].Header
		ceid := h.Get("ce-id")
		r.ev.check(id, uuidV7Re.MatchString(ceid) && h.Get("Nats-Msg-Id") == ceid, "ce-id %q, Nats-Msg-Id %q: want one UUIDv7 (P12.3)", ceid, h.Get("Nats-Msg-Id"))
		want := map[string]string{"ce-specversion": "1.0", "ce-source": r.comp.ID(), "ce-type": p.Subject, "content-type": "application/json",
			"ce-aggregatetype": own[p.Subject].AggregateType, "ce-hopcount": "0"}
		for k, v := range want {
			r.ev.check(id, h.Get(k) == v, "%s %s = %q, want %q (P12 envelope)", p.Subject, k, h.Get(k), v)
		}
		r.ev.check(id, rfc3339.MatchString(h.Get("ce-time")), "ce-time %q is not RFC 3339 UTC", h.Get("ce-time"))
		r.ev.check(id, regexp.MustCompile(`^[1-9][0-9]*$`).MatchString(h.Get("ce-aggregateversion")), "ce-aggregateversion %q", h.Get("ce-aggregateversion"))
		r.ev.check(id, strings.HasPrefix(h.Get("ce-dataschema"), r.comp.ID()+"@"+r.comp.Version()+"/contracts/events/"), "ce-dataschema %q", h.Get("ce-dataschema"))
		r.ev.check(id, h.Get("ce-causationid") == "", "an event from a request carries ce-causationid %q (P12.8)", h.Get("ce-causationid"))
		r.ev.check(id, strings.Contains(h.Get("traceparent"), tid), "traceparent %q is not the request's trace %s (P12, P18.1)", h.Get("traceparent"), tid)
	}
}

// CP-EVP-02: with the bus stopped the command still succeeds; once the bus is back the event
// is delivered, exactly once, into the stream (P12.1).
func caseEVP02(ctx context.Context, r *Run) {
	const id = "CP-EVP-02"
	ps := r.needPub(id)
	if len(ps) == 0 {
		return
	}
	p := ps[0]
	if err := r.nats.C.Stop(ctx, 1); !r.ev.require(id, err == nil, "stopping NATS: %v", err) {
		return
	}
	agg, x, err := r.produce(ctx, p)
	if startErr := r.nats.C.Start(ctx); startErr != nil {
		r.ev.fail(id, "starting NATS again: %v", startErr)
		return
	}
	if !r.ev.check(id, err == nil, "%s with the bus stopped: %v (P12.1: the outbox decouples the command from the bus)", p.Via, err) {
		return
	}
	_ = x
	got := r.bus.waitFor(eventOf(p.Subject, agg), 1, 45*time.Second)
	r.ev.check(id, len(got) >= 1, "%s about %s not published within 45 s of the bus coming back (P12.1: retried, never dropped)", p.Subject, agg)
	time.Sleep(2 * time.Second)
	n, err := r.streamCount(ctx, p.Subject, agg)
	r.ev.check(id, err == nil && n == 1, "the stream holds %d %s events about %s (%v), want exactly 1", n, p.Subject, agg, err)
}

// streamCount counts the stream's messages of a subject about one aggregate.
func (r *Run) streamCount(ctx context.Context, subject, agg string) (int, error) {
	js, err := r.js()
	if err != nil {
		return 0, err
	}
	c, err := js.OrderedConsumer(ctx, streamOf(subject), jetstream.OrderedConsumerConfig{FilterSubjects: []string{subject}})
	if err != nil {
		return 0, err
	}
	n := 0
	for {
		batch, err := c.FetchNoWait(500)
		if err != nil {
			return n, err
		}
		got := 0
		for m := range batch.Messages() {
			got++
			if m.Headers().Get("ce-subject") == agg {
				n++
			}
		}
		if got == 0 {
			return n, nil
		}
	}
}

// CP-EVP-03: with two replicas pumping the same outbox, every row is published once: no
// ce-id reaches the bus twice (P12.1, P10.9).
func caseEVP03(ctx context.Context, r *Run) {
	const id = "CP-EVP-03"
	ps := r.needPub(id)
	if len(ps) == 0 || !r.ev.require(id, r.replica != nil, "no second replica") {
		return
	}
	p := ps[0]
	var mu sync.Mutex
	var aggs []string
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if a, _, err := r.produce(ctx, p); err == nil {
				mu.Lock()
				aggs = append(aggs, a)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	time.Sleep(8 * time.Second)
	seen := map[string]int{}
	for _, a := range aggs {
		for _, m := range r.bus.find(eventOf(p.Subject, a)) {
			seen[m.Header.Get("ce-id")]++
		}
	}
	dup := 0
	for _, n := range seen {
		if n > 1 {
			dup++
		}
	}
	r.ev.check(id, len(seen) >= len(aggs), "%d of %d events reached the bus within 8 s", len(seen), len(aggs))
	r.ev.check(id, dup == 0, "%d outbox rows were published more than once by two replicas (P10.9: claim with FOR UPDATE SKIP LOCKED)", dup)
}

// streamDefaults are P12.4's defaults.
func checkStreamDefaults(cfg jetstream.StreamConfig) []string {
	var is []string
	first := strings.ToLower(strings.TrimPrefix(cfg.Name, "BE_"))
	if len(cfg.Subjects) != 1 || cfg.Subjects[0] != first+".>" {
		is = append(is, fmt.Sprintf("subjects %v, want [%s.>]", cfg.Subjects, first))
	}
	if cfg.MaxAge != 7*24*time.Hour {
		is = append(is, fmt.Sprintf("max_age %v", cfg.MaxAge))
	}
	if cfg.MaxBytes != 1<<30 {
		is = append(is, fmt.Sprintf("max_bytes %d", cfg.MaxBytes))
	}
	if cfg.Discard != jetstream.DiscardOld {
		is = append(is, "discard not old")
	}
	if cfg.Duplicates != 10*time.Minute {
		is = append(is, fmt.Sprintf("duplicate_window %v", cfg.Duplicates))
	}
	if cfg.Storage != jetstream.FileStorage || cfg.Replicas > 1 {
		is = append(is, "not file storage with 1 replica")
	}
	return is
}

// stepEVP04Migrated (CP-EVP-04, right after the migration): every stream of a published
// subject exists with the defaults; the suite then changes one setting, which the component
// must leave alone (checked by caseEVP04).
func stepEVP04Migrated(ctx context.Context, r *Run) {
	const id = "CP-EVP-04"
	if !contains(r.ran, "events-pub") || r.natsConn == nil {
		return
	}
	js, err := r.js()
	if !r.ev.require(id, err == nil, "%v", err) {
		return
	}
	for _, name := range r.publishedStreams() {
		s, err := js.Stream(ctx, name)
		if !r.ev.check(id, err == nil, "stream %s does not exist after the migration (%v) (P11.3, P12.4)", name, err) {
			continue
		}
		cfg := s.CachedInfo().Config
		for _, is := range checkStreamDefaults(cfg) {
			r.ev.fail(id, "stream %s created with %s (P12.4 defaults)", name, is)
		}
		cfg.MaxAge = 3 * 24 * time.Hour
		_, err = js.UpdateStream(ctx, cfg)
		r.ev.require(id, err == nil, "changing %s: %v", name, err)
	}
}

func (r *Run) publishedStreams() []string {
	set := map[string]bool{}
	for _, s := range r.comp.Manifest.Events.Publishes {
		set[streamOf(s)] = true
	}
	return sortedKeys(set)
}

// CP-EVP-04 (after start): the operator's change survived the component's start (P12.4:
// never change a stream that exists).
func caseEVP04(ctx context.Context, r *Run) {
	const id = "CP-EVP-04"
	if !r.needMain(id) || r.natsConn == nil {
		return
	}
	js, err := r.js()
	if !r.ev.require(id, err == nil, "%v", err) {
		return
	}
	for _, name := range r.publishedStreams() {
		s, err := js.Stream(ctx, name)
		if r.ev.check(id, err == nil, "stream %s: %v", name, err) {
			r.ev.check(id, s.CachedInfo().Config.MaxAge == 3*24*time.Hour, "stream %s max_age %v after the component started, the suite set 72h (P12.4: never changed)", name, s.CachedInfo().Config.MaxAge)
		}
	}
}

// CP-EVP-05: every payload the component published passes its contract and stays within
// 64 KiB; a command whose payload would exceed it fails as INTERNAL and publishes nothing
// (P12.2).
func caseEVP05(ctx context.Context, r *Run) {
	const id = "CP-EVP-05"
	ps := r.needPub(id)
	if len(ps) == 0 {
		return
	}
	own := r.ownEvents()
	n := 0
	for _, m := range r.bus.find(func(m busMsg) bool { return m.Header.Get("ce-source") == r.comp.ID() }) {
		def, ok := own[m.Subject]
		if !ok {
			continue // CP-EVP-06
		}
		n++
		r.ev.check(id, len(m.Data) <= 64<<10, "%s payload of %d bytes, over 64 KiB (P12.2)", m.Subject, len(m.Data))
		s, err := protoschema.CompileDoc(m.Subject, def.Payload)
		if r.ev.require(id, err == nil, "compiling the payload schema of %s: %v", m.Subject, err) {
			if err := protoschema.ValidateJSON(s, m.Data); err != nil {
				r.ev.fail(id, "%s payload does not match its contract: %v (P12.2)", m.Subject, err)
			}
		}
	}
	r.ev.check(id, n > 0, "no published payload to check")
	r.bigPayload(ctx, id, ps[0])
}

// bigPayload sends the produce operation with a string field of 70 KiB.
func (r *Run) bigPayload(ctx context.Context, id string, p Produce) {
	res, name := splitVia(p.Via)
	op := r.comp.Fixtures.Resources[res][name]
	var body map[string]any
	if json.Unmarshal(r.fixtureBody(op), &body) != nil || name != "create" {
		r.ev.pass(id, "no create body to enlarge")
		return
	}
	field := ""
	for _, k := range sortedKeys(body) {
		if _, ok := body[k].(string); ok && (op.Fingerprint == nil || contains(op.Fingerprint, k)) && field == "" {
			field = k
		}
	}
	if field == "" {
		return
	}
	body[field] = strings.Repeat("x", 70<<10)
	start := time.Now()
	x := r.invoke(ctx, op, "", "", nil, withBody([]byte(mustJSON(body))))
	if x.Status < 300 {
		agg := jsonPathString(x.Body, idPath(op))
		got := r.bus.waitFor(eventOf(p.Subject, agg), 1, 5*time.Second)
		for _, m := range got {
			r.ev.check(id, len(m.Data) <= 64<<10, "a %d-byte payload was published (P12.2: refused at publish)", len(m.Data))
		}
		r.ev.pass(id, "the enlarged field does not reach the payload")
		return
	}
	r.ev.check(id, x.Status == 500 && x.reason() == "INTERNAL", "a command whose payload exceeds 64 KiB = %d %s, want 500 INTERNAL (P12.2)", x.Status, x.reason())
	big := r.bus.find(func(m busMsg) bool { return m.At.After(start) && len(m.Data) > 64<<10 })
	r.ev.check(id, len(big) == 0, "%d payloads over 64 KiB reached the bus", len(big))
}

// CP-EVP-06: events.publishes equals the contract's subjects, and every subject the component
// published during the run is in it (P12.16).
func caseEVP06(_ context.Context, r *Run) {
	const id = "CP-EVP-06"
	declared := append([]string(nil), r.comp.Manifest.Events.Publishes...)
	sort.Strings(declared)
	contract := sortedKeys(r.ownEvents())
	r.ev.check(id, strings.Join(declared, ",") == strings.Join(contract, ","), "events.publishes %v, the contract's subjects %v (P12.16)", declared, contract)
	for _, m := range r.bus.find(func(m busMsg) bool { return m.Header.Get("ce-source") == r.comp.ID() }) {
		r.ev.check(id, contains(declared, m.Subject), "published %s, not in events.publishes (P12.16)", m.Subject)
	}
}
