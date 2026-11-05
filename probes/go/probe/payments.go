package probe

import (
	"context"
	"errors"
	"net"
	"net/url"
	"time"

	"github.com/brickKit/be-sdk-go/v3/calls"
	"github.com/brickKit/be-sdk-go/v3/config"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/errs"
	"github.com/brickKit/be-sdk-go/v3/extcmd"
	"github.com/brickKit/be-sdk-go/v3/httpx"
)

// The external commands profile's payment (since 3.2.41; be-protocol chapter 13, the assembly project's A138): a
// payment is an irrevocable external command to the suite's fake external system (conformance/external) at
// PROBE_EXTERNAL_URL; the capabilities it declares are configured, so the suite checks each against what the fake
// system really does. Without PROBE_EXTERNAL_URL there is no payment.

// OpPay is the payment's operation, after the variant's prefix.
const OpPay = "pay"

// ExternalKeys are the payment's configuration keys.
func ExternalKeys() []config.Key {
	return []config.Key{
		{Name: "PROBE_EXTERNAL_URL", Type: config.String},                                        // the fake external system; empty: no payment
		{Name: "PROBE_EXTERNAL_DEDUPE_WINDOW", Type: config.Duration, Default: time.Duration(0)}, // 0: it does not deduplicate
		{Name: "PROBE_EXTERNAL_LAST_USE", Type: config.Boolean, Default: false},
		{Name: "PROBE_EXTERNAL_MAX_PROCESSING", Type: config.Duration, Default: time.Duration(0)},
		{Name: "PROBE_EXTERNAL_IRREVOCABLE", Type: config.Boolean, Default: true},
	}
}

// ExternalCommands is the payment's adapter and capability (extcmd.Provider).
func (p *Probe) ExternalCommands() (extcmd.Adapter, map[string]extcmd.Capabilities) {
	cfg := p.rt.Config
	return &payments{p: p}, map[string]extcmd.Capabilities{OpPay: {Query: true,
		DedupeWindow: cfg.Duration("PROBE_EXTERNAL_DEDUPE_WINDOW"), LastUse: cfg.Bool("PROBE_EXTERNAL_LAST_USE"),
		MaxProcessing: cfg.Duration("PROBE_EXTERNAL_MAX_PROCESSING"), Irrevocable: cfg.Bool("PROBE_EXTERNAL_IRREVOCABLE")}}
}

type payments struct{ p *Probe }

func (a *payments) client() (*calls.HTTP, error) {
	u := a.p.rt.Config.String("PROBE_EXTERNAL_URL")
	if u == "" {
		return nil, errors.New("PROBE_EXTERNAL_URL is not set")
	}
	h, ok := a.p.rt.HTTPAt("external", u)
	if !ok {
		return nil, errors.New("the external system's address is not usable")
	}
	return h, nil
}

type external struct {
	State      string `json:"state"`
	ExternalID string `json:"external_id"`
}

// Send pays: the business reference is the external idempotency key and the payment's identity there.
func (a *payments) Send(ctx context.Context, c extcmd.Command) (extcmd.Result, error) {
	h, err := a.client()
	if err != nil {
		return extcmd.Result{Class: extcmd.NotReached, Response: err.Error()}, nil
	}
	body := map[string]any{"reference": c.Reference, "params": c.Params}
	if c.ExpiresAt != nil {
		body["expires_at"] = c.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	var out external
	err = h.Do(ctx, calls.Request{Method: "POST", Path: "/payments", Body: body, IdempotencyKey: c.Reference}, &out)
	if err != nil {
		return extcmd.Result{Class: classify(err), Response: err.Error()}, nil
	}
	return extcmd.Result{Class: out.State, ExternalID: out.ExternalID, Response: out.State}, nil
}

// classify sorts a failed send (P13.5): nothing sent (a dial failure, the circuit open, an explicit rate limit),
// refused for good (the request is wrong), or possibly processed (a timeout, a gateway error, a cut answer).
func classify(err error) string {
	var op *net.OpError
	var st *calls.StatusError
	switch {
	case errors.As(err, &op) && op.Op == "dial", errs.Is(err, "be", errs.ReasonCircuitOpen):
		return extcmd.NotReached
	case errors.As(err, &st) && st.Status == 429:
		return extcmd.NotReached
	case errors.As(err, &st) && (st.Status == 400 || st.Status == 422):
		return extcmd.Rejected
	}
	return extcmd.MaybeReached
}

// Query asks the fake system by the business reference: found in a state, or not found (404).
func (a *payments) Query(ctx context.Context, c extcmd.Command) (extcmd.Answer, error) {
	h, err := a.client()
	if err != nil {
		return extcmd.Answer{}, err
	}
	var out external
	err = h.Do(ctx, calls.Request{Method: "GET", Path: "/payments/" + url.PathEscape(c.Reference)}, &out)
	var st *calls.StatusError
	if errors.As(err, &st) && st.Status == 404 {
		return extcmd.Answer{Found: false}, nil
	}
	if err != nil {
		return extcmd.Answer{}, err
	}
	return extcmd.Answer{Found: true, State: out.State, ExternalID: out.ExternalID, Response: out.State}, nil
}

// List lists what the fake system took in a time window (extcmd.Lister, since 3.2.43): a full recovery's
// reconciliation matches one listing instead of one query per command (be-protocol P13.11).
func (a *payments) List(ctx context.Context, from, to time.Time) ([]extcmd.Record, error) {
	h, err := a.client()
	if err != nil {
		return nil, err
	}
	var out struct {
		Payments []struct {
			Reference  string `json:"reference"`
			State      string `json:"state"`
			ExternalID string `json:"external_id"`
		} `json:"payments"`
	}
	q := url.Values{"from": {from.UTC().Format(time.RFC3339Nano)}, "to": {to.UTC().Format(time.RFC3339Nano)}}
	if err := h.Do(ctx, calls.Request{Method: "GET", Path: "/payments?" + q.Encode()}, &out); err != nil {
		return nil, err
	}
	recs := make([]extcmd.Record, 0, len(out.Payments))
	for _, p := range out.Payments {
		recs = append(recs, extcmd.Record{Reference: p.Reference, State: p.State, ExternalID: p.ExternalID, Response: p.State})
	}
	return recs, nil
}

type payIn struct {
	Reference string         `json:"reference"`
	Params    map[string]any `json:"params"`
	ExpiresAt *time.Time     `json:"expires_at"`
}

// payHTTP requests a payment (POST /api/probe/payment): the command, written in this request's transaction.
func (p *Probe) payHTTP(c *httpx.Context) error {
	var in payIn
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	if in.Reference == "" || len(in.Params) == 0 {
		return invalid("reference and params are required")
	}
	var cmd extcmd.Command
	var state string
	err := p.rt.DB.Tx(c.Ctx(), func(ctx context.Context, tx db.Tx) error {
		var err error
		cmd, state, err = p.rt.External.Request(ctx, tx, p.v.ID, in.Reference, OpPay, in.Params, in.ExpiresAt)
		return err
	})
	if err != nil {
		return err
	}
	return c.JSON(200, map[string]any{"command": cmd.ID, "state": state})
}

// paymentHTTP reads a payment's command and attempts (GET /api/probe/payment/:reference).
func (p *Probe) paymentHTTP(c *httpx.Context) error {
	out := map[string]any{}
	err := p.rt.DB.Tx(c.Ctx(), func(ctx context.Context, tx db.Tx) error {
		var id, state, reason string
		var external *string
		var deadline *time.Time
		if err := tx.QueryRow(ctx, `SELECT id::text, state, coalesce(reason, ''), external_id, resend_deadline
			FROM be_external_command WHERE requester = $1 AND reference = $2`, p.v.ID, c.Param("reference")).
			Scan(&id, &state, &reason, &external, &deadline); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT kind, coalesce(result, ''), reached FROM be_external_attempt WHERE command = $1
			ORDER BY n`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		var attempts []map[string]any
		for rows.Next() {
			var kind, result string
			var reached bool
			if err := rows.Scan(&kind, &result, &reached); err != nil {
				return err
			}
			attempts = append(attempts, map[string]any{"kind": kind, "result": result, "reached": reached})
		}
		out = map[string]any{"command": id, "state": state, "reason": reason, "external_id": external,
			"resend_deadline": deadline, "attempts": attempts}
		return rows.Err()
	})
	if err != nil {
		return errs.New(errs.ReasonNotFound, "no such payment").Wrap(err)
	}
	return c.JSON(200, out)
}
