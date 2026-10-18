// Package probe is the Go probe: the acceptance suite's communication-only component (be-acceptance
// probes/CONTRACT.md). It has no business logic, so what it measures comes from the language, the topology and the
// engines only.
package probe

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/brickKit/be-sdk-go/v3/calls"
	"github.com/brickKit/be-sdk-go/v3/component"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/errs"
	"github.com/brickKit/be-sdk-go/v3/events"
	"github.com/brickKit/be-sdk-go/v3/grpcx"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/runtime"
	"github.com/brickKit/be-sdk-go/v3/work"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// Definition is one probe variant as the launcher and the shells take it.
func Definition(v Variant) component.Definition {
	return component.Definition{ID: v.ID, Version: Version, Keys: Keys(), Migrations: Migrations(), GRPCPort: v.GRPCPort,
		Streams: []events.Stream{{Name: v.Stream, Partitions: 4}}, Subscribes: true, Authz: Authz(v.ID),
		New: func(rt *runtime.Runtime) (component.Module, error) {
			// the probe's own reason: this deployment gives the probe no next probe to call
			return &Probe{rt: rt, v: v, noNext: errs.Define(v.ID, "NO_NEXT_PROBE", errs.FailedPrecondition, 409,
				errs.RetryNo, "No next probe is configured")}, nil
		}}
}

// Definitions are every Go probe variant, for the shells that compile them in.
func Definitions() []component.Definition {
	out := make([]component.Definition, len(Variants))
	for i, v := range Variants {
		out[i] = Definition(v)
	}
	return out
}

// ByID finds a variant; false when the ID is not a Go probe.
func ByID(id string) (Variant, bool) {
	for _, v := range Variants {
		if v.ID == id {
			return v, true
		}
	}
	return Variant{}, false
}

// Probe is the module.
type Probe struct {
	rt     *runtime.Runtime
	v      Variant
	noNext errs.Declared
}

func invalid(msg string) error { return errs.New(errs.ReasonRequestInvalid, msg) }

// Routes registers the user surface; with PROBE_GUARD the routes need a credential and the operation.
func (p *Probe) Routes(r *httpx.Routes) error {
	guard := p.rt.Config.Bool("PROBE_GUARD")
	add := func(method, path, op string, h httpx.Handler) {
		route := httpx.Route{Method: method, Path: "/api/probe/" + path, Handle: h, Public: !guard}
		if guard {
			route.Operation = Prefix(p.v.ID) + "." + op
		}
		r.Add(route)
	}
	add("POST", "echo", "echo", p.echoHTTP)
	add("POST", "work", "work", p.workHTTP)
	add("POST", "write", "write", p.write)
	add("POST", "call", "call", p.call)
	add("GET", "seen", "read", p.seen)
	return nil
}

type payload struct {
	Payload string `json:"payload"`
}

func (p *Probe) echoHTTP(c *httpx.Context) error {
	var in payload
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	return c.JSON(200, in)
}

type workIn struct {
	MS int `json:"ms"`
}

func (p *Probe) workHTTP(c *httpx.Context) error {
	var in workIn
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	if in.MS < 0 || in.MS > MaxWorkMS {
		return invalid("ms must be 0..1000")
	}
	burn(time.Duration(in.MS) * time.Millisecond)
	return c.JSON(200, in)
}

// burn spends d of CPU time on this goroutine (wall clock as the measure).
func burn(d time.Duration) {
	x, end := uint64(1), time.Now().Add(d)
	for time.Now().Before(end) {
		for i := 0; i < 1000; i++ {
			x = x*6364136223846793005 + 1442695040888963407
		}
	}
	sink = x
}

var sink uint64

type writeIn struct {
	Bytes int  `json:"bytes"`
	Relay bool `json:"relay"`
}

type written struct {
	ID    string `json:"id"`
	Relay bool   `json:"relay"`
	Blob  string `json:"blob,omitempty"`
}

// write inserts a row and its event in one transaction (Write; a payload over 64 KiB is a big event).
func (p *Probe) write(c *httpx.Context) error {
	var in writeIn
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	if in.Bytes < 0 || in.Bytes > MaxBytes {
		return invalid("bytes must be 0..524288")
	}
	id := ids.New().String()
	err := p.rt.DB.Tx(c.Ctx(), func(ctx context.Context, tx db.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO probe_writes (id, size) VALUES ($1, $2)", id, in.Bytes); err != nil {
			return err
		}
		_, err := p.rt.Events.Emit(ctx, tx, p.v.Stream, events.Event{Type: WrittenType, RecordID: id, Version: 1,
			Projection: events.Full, Data: written{ID: id, Relay: in.Relay, Blob: strings.Repeat("w", in.Bytes)}})
		return err
	})
	if err != nil {
		return err
	}
	return c.JSON(201, map[string]string{"id": id})
}

type callIn struct {
	Op      string `json:"op"`      // echo, work
	Surface string `json:"surface"` // grpc, http
	Bytes   int    `json:"bytes"`
	MS      int    `json:"ms"`
}

// call makes one call to PROBE_NEXT on the surface asked for.
func (p *Probe) call(c *httpx.Context) error {
	var in callIn
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	if in.Bytes < 0 || in.Bytes > MaxBytes || in.MS < 0 || in.MS > MaxWorkMS {
		return invalid("bytes must be 0..524288 and ms 0..1000")
	}
	next := p.rt.Config.String("PROBE_NEXT")
	if next == "" {
		return p.noNext.New("PROBE_NEXT is not set")
	}
	var err error
	start := time.Now()
	switch {
	case in.Surface == "grpc" && in.Op == "echo":
		err = p.echoNext(c.Ctx(), next, in.Bytes)
	case in.Surface == "grpc" && in.Op == "work":
		err = p.grpcNext(c.Ctx(), next, WorkMethod, wrapperspb.UInt32(uint32(in.MS)), new(wrapperspb.UInt32Value))
	case in.Surface == "http" && (in.Op == "echo" || in.Op == "work"):
		err = p.httpNext(c.Ctx(), next, in)
	default:
		return invalid("op must be echo or work, surface grpc or http")
	}
	if err != nil {
		return err
	}
	// the hop alone, as the caller sees it: the driver's own request to this probe is not in it
	return c.JSON(200, map[string]any{"ok": true, "elapsed_us": time.Since(start).Microseconds()})
}

func (p *Probe) grpcNext(ctx context.Context, next, method string, in, out any) error {
	conn, ok := p.rt.GRPCAt(next, p.rt.Config.String("PROBE_NEXT_GRPC_URL"))
	if !ok {
		return p.noNext.New("PROBE_NEXT_GRPC_URL is not set")
	}
	return conn.Invoke(ctx, method, in, out, calls.Idempotent())
}

func (p *Probe) echoNext(ctx context.Context, next string, n int) error {
	out := new(wrapperspb.BytesValue)
	if err := p.grpcNext(ctx, next, EchoMethod, wrapperspb.Bytes(make([]byte, n)), out); err != nil {
		return err
	}
	if len(out.Value) != n {
		return errors.New("the echo came back with another size")
	}
	return nil
}

func (p *Probe) httpNext(ctx context.Context, next string, in callIn) error {
	h, ok := p.rt.HTTPAt(next, p.rt.Config.String("PROBE_NEXT_URL"))
	if !ok {
		return p.noNext.New("PROBE_NEXT_URL is not set")
	}
	req := calls.Request{Method: "POST", Idempotent: true, ForwardUser: p.rt.Config.Bool("PROBE_GUARD")}
	if in.Op == "echo" {
		req.Path, req.Body = "/api/probe/echo", payload{Payload: strings.Repeat("e", in.Bytes)}
	} else {
		req.Path, req.Body = "/api/probe/work", workIn{MS: in.MS}
	}
	var out json.RawMessage
	return h.Do(ctx, req, &out)
}

func (p *Probe) seen(c *httpx.Context) error {
	var count, big, relayed int
	err := p.rt.DB.Tx(c.Ctx(), func(ctx context.Context, tx db.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE size > 65536),
			count(*) FILTER (WHERE relayed_at IS NOT NULL) FROM probe_seen`).Scan(&count, &big, &relayed)
	})
	if err != nil {
		return err
	}
	return c.JSON(200, map[string]int{"count": count, "big": big, "relayed": relayed})
}

// Services registers the system surface.
func (p *Probe) Services(s *grpcx.Services) error {
	s.Register(&desc, p, map[string]grpcx.Method{"Echo": {Operation: Prefix(p.v.ID) + ".echo", Callers: Callers},
		"Work": {Operation: Prefix(p.v.ID) + ".work", Callers: Callers}})
	return nil
}

// Echo implements the system method.
func (p *Probe) Echo(_ context.Context, in *wrapperspb.BytesValue) (*wrapperspb.BytesValue, error) {
	return in, nil
}

// Work implements the system method.
func (p *Probe) Work(_ context.Context, in *wrapperspb.UInt32Value) (*wrapperspb.UInt32Value, error) {
	if in.Value > MaxWorkMS {
		return nil, invalid("ms must be 0..1000")
	}
	burn(time.Duration(in.Value) * time.Millisecond)
	return in, nil
}

// Subscriptions consumes the peer's stream when PROBE_PEER_STREAM names it.
func (p *Probe) Subscriptions(s *events.Subscriptions) error {
	peer := p.rt.Config.String("PROBE_PEER_STREAM")
	if peer == "" {
		return nil
	}
	s.Add(events.Sub{Name: "peer", Streams: []string{peer}, Types: []string{WrittenType}, Handle: p.received})
	return nil
}

func (p *Probe) received(ctx context.Context, tx db.Tx, e events.Received) error {
	var w written
	if err := e.Decode(&w); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO probe_seen (event_id, source, size, relay) VALUES ($1, $2, $3, $4)",
		e.ID, e.Source, len(w.Blob), w.Relay); err != nil {
		return err
	}
	if !w.Relay {
		return nil
	}
	// Relay: the call to the next probe leaves the transaction as a work item (no network call inside it)
	_, err := p.rt.Work.Enqueue(ctx, tx, "probe.relay", map[string]string{"event": e.ID})
	return err
}

// Jobs declares the relay.
func (p *Probe) Jobs(j *work.Jobs) error {
	j.Add(work.Job{Name: "probe.relay", Kind: work.Queue, Effect: work.IdempotentExternal, Timeout: 10 * time.Second,
		Handle: p.relay})
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
