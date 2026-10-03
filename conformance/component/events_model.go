package compconf

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	beprotocol "github.com/brickKit/be-protocol"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Events: contracts, names, the bus recorder and publishing as a producer (P12).

// eventDef is one entry of a contracts/events/*.events.json.
type eventDef struct {
	Subject       string          `json:"subject"`
	AggregateType string          `json:"x-aggregate-type"`
	TxDocument    bool            `json:"x-transaction-document"`
	Payload       json.RawMessage `json:"payload"`
	file          string
	producer      string
}

// loadEvents reads every *.events.json under dir of fsys.
func loadEvents(fsys fs.FS, dir string) map[string]eventDef {
	out := map[string]eventDef{}
	_ = fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".events.json") {
			return nil
		}
		b, _ := fs.ReadFile(fsys, p)
		var doc struct {
			Events []eventDef `json:"events"`
		}
		if json.Unmarshal(b, &doc) == nil {
			for _, e := range doc.Events {
				e.file = path.Base(p)
				out[e.Subject] = e
			}
		}
		return nil
	})
	return out
}

// ownEvents are the component's event contract.
func (r *Run) ownEvents() map[string]eventDef { return loadEvents(r.comp.FS, "contracts") }

// depEvents are the event contracts of the dependencies (be-protocol's fixtures for
// conformance/*, else --dep-contracts), keyed by subject.
func (r *Run) depEvents() map[string]eventDef {
	out := map[string]eventDef{}
	for _, d := range r.comp.Dependencies() {
		var m map[string]eventDef
		switch {
		case r.opt.DepContracts[d.ID] != "":
			m = loadEvents(os.DirFS(r.opt.DepContracts[d.ID]), ".")
		case strings.HasPrefix(d.ID, "conformance/"):
			sub, err := fs.Sub(beprotocol.FS, "fixtures/"+strings.TrimPrefix(d.ID, "conformance/")+"/contracts")
			if err == nil {
				m = loadEvents(sub, ".")
			}
		}
		for k, v := range m {
			v.producer = d.ID
			out[k] = v
		}
	}
	return out
}

// durableName is P12.5's name of the component's durable for a subject.
func durableName(id, subject string) string {
	return strings.ReplaceAll(id, "/", "_") + "__" + strings.ReplaceAll(subject, ".", "__")
}

// streamOf is P12.4's stream of a subject.
func streamOf(subject string) string {
	first, _, _ := strings.Cut(subject, ".")
	return "BE_" + strings.ToUpper(first)
}

// busMsg is one message the suite saw on the bus.
type busMsg struct {
	Subject string
	Header  nats.Header
	Data    []byte
	At      time.Time
}

// busRecorder keeps every message published while the suite is connected.
type busRecorder struct {
	mu   sync.Mutex
	msgs []busMsg
}

func (b *busRecorder) add(m *nats.Msg) {
	if strings.HasPrefix(m.Subject, "$") || strings.HasPrefix(m.Subject, "_INBOX") {
		return
	}
	b.mu.Lock()
	b.msgs = append(b.msgs, busMsg{Subject: m.Subject, Header: m.Header, Data: append([]byte(nil), m.Data...), At: time.Now()})
	b.mu.Unlock()
}

// find returns the messages matching pred.
func (b *busRecorder) find(pred func(busMsg) bool) []busMsg {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []busMsg
	for _, m := range b.msgs {
		if pred(m) {
			out = append(out, m)
		}
	}
	return out
}

// waitFor polls the recorder until pred matches at least n messages or d passes.
func (b *busRecorder) waitFor(pred func(busMsg) bool, n int, d time.Duration) []busMsg {
	end := time.Now().Add(d)
	for {
		got := b.find(pred)
		if len(got) >= n || time.Now().After(end) {
			return got
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// js is a JetStream handle on the suite's connection.
func (r *Run) js() (jetstream.JetStream, error) {
	if r.natsConn == nil {
		return nil, fmt.Errorf("no bus")
	}
	return jetstream.New(r.natsConn)
}

// sample is one consumed subject's sample, re-addressed to a fresh aggregate.
type sample struct {
	c       Consume
	def     eventDef
	aggID   string
	payload map[string]any
}

// newSample loads a consumes entry's sample with {sub:...} substituted and, when the entry
// names the aggregate ID's JSONPath, a fresh aggregate ID unless keepID.
func (r *Run) newSample(c Consume, keepID bool) (*sample, error) {
	b, err := r.comp.FixtureFile(c.SampleFile)
	if err != nil {
		return nil, err
	}
	s := &sample{c: c, def: r.depEvents()[c.Subject]}
	if err := json.Unmarshal([]byte(r.substitute(string(b), "")), &s.payload); err != nil {
		return nil, err
	}
	field := strings.TrimPrefix(c.AggregateID, "$.")
	if field == "" {
		field = "id"
	}
	if !keepID {
		s.payload[field] = "cc-" + uuidv7()
	}
	s.aggID, _ = s.payload[field].(string)
	return s, nil
}

// at returns the payload for aggregate version v: the version field and every other string
// field carry the version, so states of different versions differ.
func (s *sample) at(v int, field string) []byte {
	p := map[string]any{}
	for k, x := range s.payload {
		p[k] = x
	}
	if _, ok := p["version"]; ok {
		p["version"] = v
	}
	if field != "" {
		if str, ok := p[field].(string); ok {
			p[field] = str + "-v" + strconv.Itoa(v)
		}
	}
	b, _ := json.Marshal(p)
	return b
}

// variedField is a string field of the payload that is neither the aggregate ID nor a code
// with a pattern in the contract (display text the suite may change per version).
func (s *sample) variedField() string {
	var schema struct {
		Properties map[string]struct {
			Type    any    `json:"type"`
			Pattern string `json:"pattern"`
			Format  string `json:"format"`
		} `json:"properties"`
	}
	_ = json.Unmarshal(s.def.Payload, &schema)
	field := strings.TrimPrefix(s.c.AggregateID, "$.")
	for _, k := range sortedKeys(s.payload) {
		pr := schema.Properties[k]
		if _, ok := s.payload[k].(string); ok && k != field && pr.Pattern == "" && pr.Format == "" && !strings.HasSuffix(k, "_id") {
			return k
		}
	}
	return ""
}

// publishAs publishes a sample version as its producer would (P12 envelope); headers may
// override or delete (value "") envelope headers. It returns the ce-id.
func (r *Run) publishAs(ctx context.Context, s *sample, version int, data []byte, headers map[string]string) (string, error) {
	js, err := r.js()
	if err != nil {
		return "", err
	}
	id := uuidv7()
	m := nats.NewMsg(s.c.Subject)
	m.Data = data
	h := map[string]string{
		"ce-specversion": "1.0", "ce-id": id, "ce-source": s.def.producer, "ce-type": s.c.Subject,
		"ce-time": time.Now().UTC().Format(time.RFC3339Nano), "ce-subject": s.aggID, "content-type": "application/json",
		"ce-dataschema":    s.def.producer + "@1.0.0/contracts/events/" + s.def.file + "#" + s.c.Subject,
		"ce-aggregatetype": s.def.AggregateType, "ce-aggregateversion": strconv.Itoa(version), "ce-hopcount": "0",
		"Nats-Msg-Id": id,
	}
	if le, ok := s.payload["legal_entity_id"].(string); ok && s.def.TxDocument {
		h["ce-legalentity"] = le
	}
	for k, v := range headers {
		h[k] = v
	}
	for k, v := range h {
		if v != "" {
			m.Header.Set(k, v)
		}
	}
	if m.Header.Get("ce-id") != "" {
		id = m.Header.Get("ce-id")
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err = js.PublishMsg(cctx, m)
	return id, err
}

// observe runs a consumes entry's observe.sql for an aggregate and returns the row as text
// ("" when no row).
func (r *Run) observe(ctx context.Context, sql, aggID string) (string, error) {
	conn, err := r.dbConn(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, sql, aggID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		v, err := rows.Values()
		if err != nil {
			return "", err
		}
		out = append(out, fmt.Sprint(v))
	}
	return strings.Join(out, "\n"), rows.Err()
}

// waitObserve polls observe until it equals want ("" = until it is not empty) or d passes.
func (r *Run) waitObserve(ctx context.Context, sql, aggID, want string, d time.Duration) string {
	end := time.Now().Add(d)
	for {
		got, _ := r.observe(ctx, sql, aggID)
		if (want == "" && got != "") || (want != "" && got == want) || time.Now().After(end) {
			return got
		}
		time.Sleep(300 * time.Millisecond)
	}
}
