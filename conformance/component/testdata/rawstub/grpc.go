package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/protoadapt"
)

// rawMsg carries protobuf bytes; the two messages of rawstub.proto are encoded by hand.
type rawMsg struct{ b []byte }

type rawCodec struct{}

func (rawCodec) Name() string { return "proto" }

func (rawCodec) Marshal(v any) ([]byte, error) {
	m, ok := v.(*rawMsg)
	if !ok {
		return nil, fmt.Errorf("rawCodec: unexpected %T", v)
	}
	return m.b, nil
}

func (rawCodec) Unmarshal(data []byte, v any) error {
	m, ok := v.(*rawMsg)
	if !ok {
		return fmt.Errorf("rawCodec: unexpected %T", v)
	}
	m.b = append([]byte(nil), data...)
	return nil
}

// decodeGetNoteRequest reads field 1 (id, string) and skips anything else.
func decodeGetNoteRequest(b []byte) (string, error) {
	id := ""
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return "", protowire.ParseError(n)
		}
		b = b[n:]
		if num == 1 && typ == protowire.BytesType {
			v, n := protowire.ConsumeString(b)
			if n < 0 {
				return "", protowire.ParseError(n)
			}
			id, b = v, b[n:]
			continue
		}
		n = protowire.ConsumeFieldValue(num, typ, b)
		if n < 0 {
			return "", protowire.ParseError(n)
		}
		b = b[n:]
	}
	return id, nil
}

func encodeNote(n note) []byte {
	var b []byte
	for i, s := range []string{n.ID, n.Title, n.OwnerID, n.CreatedAt} {
		if s == "" {
			continue
		}
		b = protowire.AppendTag(b, protowire.Number(i+1), protowire.BytesType)
		b = protowire.AppendString(b, s)
	}
	return b
}

const noteService = "conformance.rawstub.v1.NoteService"

// rpc is one method of NoteService with the facts the runtime needs before component code:
// whether it is user-facing (P7.3) and its batch-limited repeated field (P7.10).
type rpc struct {
	name       string
	userFacing bool
	batchField protowire.Number // 0 = none
	batchName  string
	batchMax   int
	handle     func(*App, context.Context, *rawMsg) (any, error)
}

var rpcs = []rpc{
	{name: "GetNote", handle: (*App).grpcGetNote},
	{name: "BatchGetNotes", batchField: 1, batchName: "ids", batchMax: 100, handle: (*App).grpcBatchGetNotes},
	{name: "ArchiveNote", userFacing: true},
}

func rpcByMethod(full string) (rpc, bool) {
	_, m := splitMethod(full)
	for _, x := range rpcs {
		if x.name == m {
			return x, true
		}
	}
	return rpc{}, false
}

func newGRPCServer(a *App) *grpc.Server {
	desc := grpc.ServiceDesc{ServiceName: noteService, HandlerType: (*any)(nil), Metadata: "conformance/rawstub/v1/rawstub.proto"}
	for _, x := range rpcs {
		x := x
		desc.Methods = append(desc.Methods, grpc.MethodDesc{
			MethodName: x.name,
			Handler: func(srv any, ctx context.Context, dec func(any) error, ic grpc.UnaryServerInterceptor) (any, error) {
				in := &rawMsg{}
				if err := dec(in); err != nil {
					return nil, err
				}
				info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/" + noteService + "/" + x.name}
				return ic(ctx, in, info, func(ctx context.Context, req any) (any, error) {
					if x.handle == nil {
						return nil, internalErr(errors.New(x.name + " has no handler"))
					}
					return x.handle(a, ctx, req.(*rawMsg))
				})
			},
		})
	}
	kp := keepalive.ServerParameters{MaxConnectionAge: a.cfg.GRPCMaxConnAge, MaxConnectionAgeGrace: 30 * time.Second}
	if a.broken == "no-goaway" {
		kp = keepalive.ServerParameters{}
	}
	s := grpc.NewServer(
		grpc.ForceServerCodec(rawCodec{}),
		grpc.MaxRecvMsgSize(4<<20),
		grpc.KeepaliveParams(kp),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 20 * time.Second, PermitWithoutStream: false}),
		grpc.UnaryInterceptor(a.grpcInterceptor),
	)
	s.RegisterService(&desc, a)
	return s
}

// countField counts the occurrences of a field number in a message.
func countField(b []byte, field protowire.Number) int {
	n := 0
	for len(b) > 0 {
		num, typ, l := protowire.ConsumeTag(b)
		if l < 0 {
			return n
		}
		b = b[l:]
		if num == field {
			n++
		}
		l = protowire.ConsumeFieldValue(num, typ, b)
		if l < 0 {
			return n
		}
		b = b[l:]
	}
	return n
}

// beforeHandler is the runtime's part after identity and the deadline floor: a user-facing
// rpc is refused (P7.3), then the batch limit (P7.10).
func beforeHandler(info *grpc.UnaryServerInfo, req any) *apiError {
	x, ok := rpcByMethod(info.FullMethod)
	if !ok {
		return nil
	}
	if x.userFacing {
		return beErr("TOKEN_INVALID", nil)
	}
	if x.batchField != 0 {
		if m, ok := req.(*rawMsg); ok {
			if got := countField(m.b, x.batchField); got > x.batchMax {
				e := beErr("BATCH_TOO_LARGE", map[string]string{"field": x.batchName, "max": strconv.Itoa(x.batchMax), "got": strconv.Itoa(got)})
				e.Violations = []violation{{Field: x.batchName, Reason: "BATCH_TOO_LARGE"}}
				return e
			}
		}
	}
	return nil
}

func (a *App) grpcBatchGetNotes(ctx context.Context, in *rawMsg) (any, error) {
	var ids []string
	b := in.b
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, ownErr("NOTE_ID_INVALID", map[string]string{"id": ""})
		}
		b = b[n:]
		if num == 1 && typ == protowire.BytesType {
			v, n := protowire.ConsumeString(b)
			if n < 0 {
				return nil, ownErr("NOTE_ID_INVALID", map[string]string{"id": ""})
			}
			ids, b = append(ids, v), b[n:]
			continue
		}
		n = protowire.ConsumeFieldValue(num, typ, b)
		if n < 0 {
			return nil, ownErr("NOTE_ID_INVALID", map[string]string{"id": ""})
		}
		b = b[n:]
	}
	var out []byte
	for _, id := range ids {
		n, err := a.getNoteByID(ctx, id)
		var ae *apiError
		if errors.As(err, &ae) && (ae.Reason == "NOT_FOUND" || ae.Reason == "NOTE_ID_INVALID") {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = protowire.AppendTag(out, 1, protowire.BytesType)
		out = protowire.AppendBytes(out, encodeNote(n))
	}
	return &rawMsg{b: out}, nil
}

func (a *App) grpcGetNote(ctx context.Context, in *rawMsg) (any, error) {
	id, err := decodeGetNoteRequest(in.b)
	if err != nil {
		return nil, ownErr("NOTE_ID_INVALID", map[string]string{"id": ""})
	}
	n, err := a.getNoteByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return &rawMsg{b: encodeNote(n)}, nil
}

// grpcInterceptor applies P7.4 in order: panic recovery → identity (be-caller) → deadline
// floor → error-detail normalisation (P4.2) → RED metrics → tracing, with the gRPC access log.
func (a *App) grpcInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (resp any, err error) {
	start := time.Now()
	md, _ := metadata.FromIncomingContext(ctx)
	first := func(k string) string {
		if v := md.Get(k); len(v) > 0 {
			return v[0]
		}
		return ""
	}
	service, method := splitMethod(info.FullMethod)
	span := startSpan(service+"/"+method, first("traceparent"), first("tracestate"))
	reqID := first("x-request-id")
	if reqID == "" {
		reqID = span.TraceID
	}
	caller := first("be-caller")
	var ae *apiError
	func() {
		defer func() {
			if p := recover(); p != nil {
				ae = internalErr(errors.New("panic: " + toString(p)))
			}
		}()
		if caller == "" {
			ae = beErr("MISSING_CALLER", nil)
			return
		}
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
		}
		if ae = beforeHandler(info, req); ae != nil {
			return
		}
		var herr error
		resp, herr = h(ctx, req)
		if herr != nil {
			ae = asAPIError(classifyDB(ctx, herr))
		}
	}()
	code := "OK"
	if ae != nil {
		resp, err = nil, a.grpcStatus(ae)
		code = ae.normalized().Code
	}
	a.metrics.Inc("be_grpc_server_handled_total", service, method, code)
	a.metrics.Observe("be_grpc_server_duration_seconds", time.Since(start).Seconds(), service, method)
	span.Attrs["rpc.system"], span.Attrs["rpc.service"], span.Attrs["rpc.method"] = "grpc", service, method
	span.Attrs["rpc.grpc.status_code"] = grpcCodes[code].num
	span.Error = code == "INTERNAL" || code == "UNKNOWN" || code == "DATA_LOSS"
	a.exporter.Finish(span)
	a.logGRPC(span, reqID, caller, service, method, code, ae, time.Since(start))
	return resp, err
}

func splitMethod(full string) (string, string) {
	full = strings.TrimPrefix(full, "/")
	i := strings.LastIndex(full, "/")
	if i < 0 {
		return full, ""
	}
	return full[:i], full[i+1:]
}

// grpcStatus is P4.2: the canonical code, the default-language detail as the message, and
// ErrorInfo always (BadRequest for a field error).
func (a *App) grpcStatus(e *apiError) error {
	n := e.normalized()
	_, detail := n.texts(a.cfg.DefaultLocale)
	if a.broken == "leak-internal" && n.Reason == "INTERNAL" && n.cause != nil {
		detail = rootCause(n.cause).Error()
	}
	st := status.New(codes.Code(grpcCodes[n.Code].num), detail)
	details := []protoadapt.MessageV1{&errdetails.ErrorInfo{Reason: n.Reason, Domain: n.Domain, Metadata: n.Metadata}}
	if n.Reason == "NOTE_ID_INVALID" {
		details = append(details, &errdetails.BadRequest{FieldViolations: []*errdetails.BadRequest_FieldViolation{
			{Field: "id", Description: detail, Reason: n.Reason}}})
	}
	for _, v := range n.Violations {
		details = append(details, &errdetails.BadRequest{FieldViolations: []*errdetails.BadRequest_FieldViolation{
			{Field: v.Field, Description: detail, Reason: v.Reason}}})
	}
	if withD, err := st.WithDetails(details...); err == nil {
		st = withD
	}
	return st.Err()
}

func (a *App) logGRPC(span *Span, reqID, caller, service, method, code string, ae *apiError, dur time.Duration) {
	f := F{"trace_id": span.TraceID, "span_id": span.SpanID, "request_id": reqID,
		"rpc.service": service, "rpc.method": method, "rpc.grpc.status_code": grpcCodes[code].num,
		"duration_ms": dur.Milliseconds()}
	if caller != "" {
		f["caller"] = caller
	}
	level := "info"
	if ae != nil {
		n := ae.normalized()
		f["error.code"], f["error.reason"], f["error"] = n.Code, n.Reason, rootCause(ae).Error()
		if l := levelForCode(n.Code); l != "none" {
			level = l
		}
	}
	a.log.Log(level, "grpc_request", f)
}
