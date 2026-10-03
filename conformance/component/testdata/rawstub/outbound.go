package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
)

// Calls to the dependency conformance/peer (P7.2, P7.6–P7.9, P8.1, P8.2): one lazily dialled
// connection per dependency, the deadline min(3 s, remaining − 50 ms), a bulkhead of 64, the
// contract's retry policy and budget, and the system-plane metadata.

const (
	peerID      = "conformance/peer"
	peerService = "conformance.peer.v1.PeerService"
	bulkhead    = 64
)

// peerServiceConfig retries the peer's methods, all NO_SIDE_EFFECTS or IDEMPOTENT in its
// contract, on UNAVAILABLE, with the budget of P7.8.
var peerServiceConfig = `{"methodConfig":[{"name":[` +
	`{"service":"` + peerService + `","method":"Reserve"},{"service":"` + peerService + `","method":"GetReservationStatus"},` +
	`{"service":"` + peerService + `","method":"BatchGetOwners"},{"service":"` + peerService + `","method":"ListOwners"},` +
	`{"service":"` + peerService + `","method":"Notify"}],` +
	`"retryPolicy":{"maxAttempts":3,"initialBackoff":"0.05s","maxBackoff":"0.5s","backoffMultiplier":2,"retryableStatusCodes":["UNAVAILABLE"]}}],` +
	`"retryThrottling":{"maxTokens":10,"tokenRatio":0.1}}`

// Peer is the client side of one dependency.
type Peer struct {
	grpcTarget, httpBase string
	once                 sync.Once
	conn                 *grpc.ClientConn
	dialErr              error
	sem                  chan struct{}
	http                 *http.Client
}

func newPeer(cfg *Config) *Peer {
	return &Peer{grpcTarget: strings.TrimPrefix(cfg.PeerGRPCEndpoint, "http://"), httpBase: cfg.PeerEndpoint,
		sem: make(chan struct{}, bulkhead), http: &http.Client{}}
}

func (p *Peer) dial() (*grpc.ClientConn, error) {
	p.once.Do(func() {
		p.conn, p.dialErr = grpc.NewClient(p.grpcTarget,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithDefaultServiceConfig(peerServiceConfig),
			grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 30 * time.Second, Timeout: 10 * time.Second}),
			grpc.WithDefaultCallOptions(grpc.ForceCodec(rawCodec{})))
	})
	return p.conn, p.dialErr
}

// outboundCtx applies P7.7 and the bulkhead of P7.9; release must be called.
func (p *Peer) outboundCtx(ctx context.Context) (context.Context, func(), error) {
	d := 3 * time.Second
	if dl, ok := ctx.Deadline(); ok {
		rem := time.Until(dl)
		if rem <= 50*time.Millisecond {
			return nil, nil, beErr("DEADLINE_BUDGET_EXHAUSTED", nil)
		}
		d = min(d, rem-50*time.Millisecond)
	}
	if broken == "no-deadline" {
		d = time.Hour
		ctx = context.WithoutCancel(ctx)
	}
	select {
	case p.sem <- struct{}{}:
	default:
		return nil, nil, beErr("OUTBOUND_LIMIT", map[string]string{"target": peerID})
	}
	cctx, cancel := context.WithTimeout(ctx, d)
	return cctx, func() { cancel(); <-p.sem }, nil
}

// call invokes one peer rpc for the request rc (nil for background work).
func (p *Peer) call(ctx context.Context, rc *reqCtx, method string, req []byte) ([]byte, error) {
	conn, err := p.dial()
	if err != nil {
		return nil, internalErr(err)
	}
	cctx, release, err := p.outboundCtx(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	kv := []string{"be-caller", componentID}
	if rc != nil {
		kv = append(kv, "traceparent", "00-"+rc.span.TraceID+"-"+rc.span.SpanID+"-01", "x-request-id", rc.reqID)
		if rc.claims != nil {
			kv = append(kv, "be-actor-sub", rc.claims.Sub)
		}
	}
	cctx = metadata.AppendToOutgoingContext(cctx, kv...)
	out := &rawMsg{}
	err = conn.Invoke(cctx, "/"+peerService+"/"+method, &rawMsg{b: req}, out)
	if err != nil {
		// At the end of the outbound budget gRPC does not always report DEADLINE_EXCEEDED: a
		// stream reset at that moment comes back as CANCELLED or INTERNAL. The budget ran out
		// either way (P7.7), as the HTTP path below already says; without this CP-OUT-07 got
		// 500 INTERNAL from a hung peer once in 17 rounds.
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			return nil, beErrCause("DEADLINE_BUDGET_EXHAUSTED", err)
		}
		return nil, peerError(err)
	}
	return out.b, nil
}

// peerError: an answer with the peer's ErrorInfo is relayed (P4.9); UNAVAILABLE without one is
// DEPENDENCY_UNAVAILABLE naming the peer; a deadline is the outbound budget's end.
func peerError(err error) error {
	st := status.Convert(err)
	for _, d := range st.Details() {
		if ei, ok := d.(*errdetails.ErrorInfo); ok {
			name := strings.ReplaceAll(strings.ToUpper(st.Code().String()), " ", "_")
			return &apiError{Code: grpcCodeName(st.Code(), name), Reason: ei.Reason, Domain: ei.Domain, Metadata: ei.Metadata, cause: err}
		}
	}
	switch st.Code() {
	case codes.Unavailable:
		e := beErrCause("DEPENDENCY_UNAVAILABLE", err)
		e.Metadata = map[string]string{"dependency": peerID}
		return e
	case codes.DeadlineExceeded:
		return beErrCause("DEADLINE_BUDGET_EXHAUSTED", err)
	case codes.ResourceExhausted:
		return beErrCause("OUTBOUND_LIMIT", err)
	}
	return internalErr(err)
}

func grpcCodeName(c codes.Code, fallback string) string {
	for name, v := range grpcCodes {
		if v.num == int(c) {
			return name
		}
	}
	return fallback
}

// noteOwner is GET /notes/{id}/owner: the note's owner from the peer's BatchGetOwners.
func (a *App) noteOwner(rc *reqCtx) error {
	n, err := a.getNoteByID(rc.ctx, rc.params["id"])
	if err != nil {
		return err
	}
	req := protowire.AppendTag(nil, 1, protowire.BytesType)
	req = protowire.AppendString(req, n.OwnerID)
	resp, err := a.peer.call(rc.ctx, rc, "BatchGetOwners", req)
	if err != nil {
		return err
	}
	owner := map[string]string{"owner_id": n.OwnerID, "display_name": ""}
	forEachField(resp, func(num protowire.Number, v []byte) {
		if num == 1 {
			forEachField(v, func(f protowire.Number, s []byte) {
				if f == 2 {
					owner["display_name"] = string(s)
				}
			})
		}
	})
	return writeJSON(rc, http.StatusOK, owner)
}

// forEachField calls f for every length-delimited field of a message.
func forEachField(b []byte, f func(protowire.Number, []byte)) {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return
		}
		b = b[n:]
		if typ == protowire.BytesType {
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return
			}
			f(num, v)
			b = b[n:]
			continue
		}
		n = protowire.ConsumeFieldValue(num, typ, b)
		if n < 0 {
			return
		}
		b = b[n:]
	}
}

// forwarded are the headers user-plane HTTP passes on unchanged (P8.1).
var forwarded = []string{"Authorization", "X-Request-Id", "traceparent", "tracestate", "baggage", "X-Authz-Revision"}

// notePeerOwner is GET /notes/{id}/peer-owner: the owner's profile from the peer's user plane,
// read with the caller's own token (P8.1, P8.2).
func (a *App) notePeerOwner(rc *reqCtx) error {
	n, err := a.getNoteByID(rc.ctx, rc.params["id"])
	if err != nil {
		return err
	}
	cctx, release, err := a.peer.outboundCtx(rc.ctx)
	if err != nil {
		return err
	}
	defer release()
	req, _ := http.NewRequestWithContext(cctx, http.MethodGet, a.peer.httpBase+"/conformance/peer/owners/"+n.OwnerID, nil)
	for _, h := range forwarded {
		if v := rc.r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	if req.Header.Get("X-Request-Id") == "" {
		req.Header.Set("X-Request-Id", rc.reqID)
	}
	if req.Header.Get("traceparent") == "" {
		req.Header.Set("traceparent", "00-"+rc.span.TraceID+"-"+rc.span.SpanID+"-01")
	}
	resp, err := a.peer.http.Do(req)
	if err != nil {
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			return beErrCause("DEADLINE_BUDGET_EXHAUSTED", err)
		}
		e := beErrCause("DEPENDENCY_UNAVAILABLE", err)
		e.Metadata = map[string]string{"dependency": peerID}
		return e
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		var prob struct{ Reason, Domain, Code string }
		if json.Unmarshal(body, &prob) == nil && prob.Reason != "" && prob.Domain != "" {
			return &apiError{Code: prob.Code, Reason: prob.Reason, Domain: prob.Domain, cause: fmt.Errorf("peer answered %d", resp.StatusCode)}
		}
		e := beErrCause("DEPENDENCY_UNAVAILABLE", fmt.Errorf("peer answered %d", resp.StatusCode))
		e.Metadata = map[string]string{"dependency": peerID}
		return e
	}
	rc.w.Header().Set("Content-Type", "application/json")
	rc.w.WriteHeader(http.StatusOK)
	_, _ = rc.w.Write(body)
	return nil
}
