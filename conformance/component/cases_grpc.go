package compconf

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/brickKit/be-acceptance/conformance/component/fakes"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// The grpc profile: the server side of the system plane (P7.2–P7.5, P7.10).

// grpcConn dials the main instance's grpc port.
func (r *Run) grpcConn(id string, opts ...grpc.DialOption) (*grpc.ClientConn, bool) {
	if !r.needMain(id) {
		return nil, false
	}
	if r.grpcAddr == "" {
		r.ev.fail(id, "the grpc port is not published")
		return nil, false
	}
	cc, err := grpc.NewClient(r.grpcAddr, append([]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}, opts...)...)
	if err != nil {
		r.ev.fail(id, "dialling the grpc port: %v", err)
		return nil, false
	}
	return cc, true
}

// invokeRaw calls method with in; caller "" sends no be-caller.
func invokeRaw(ctx context.Context, cc *grpc.ClientConn, method string, md protoreflect.MethodDescriptor, in *dynamicpb.Message, caller, rid string) *status.Status {
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	kv := []string{"x-request-id", rid}
	if caller != "" {
		kv = append(kv, "be-caller", caller)
	}
	cctx = metadata.AppendToOutgoingContext(cctx, kv...)
	err := cc.Invoke(cctx, "/"+method, in, dynamicpb.NewMessage(md.Output()), grpc.MaxCallSendMsgSize(64<<20))
	return status.Convert(err)
}

// CP-RPC-01: every method answers UNAUTHENTICATED / MISSING_CALLER without be-caller (P7.2).
func caseRPC01(ctx context.Context, r *Run) {
	const id = "CP-RPC-01"
	cc, ok := r.grpcConn(id)
	if !ok {
		return
	}
	defer cc.Close()
	for _, name := range sortedKeys(r.methods) {
		md := r.methods[name]
		st := invokeRaw(ctx, cc, name, md, dynamicpb.NewMessage(md.Input()), "", "compconf-rpc01")
		ei := errorInfo(st)
		r.recordGRPC(ei)
		r.ev.check(id, st.Code() == codes.Unauthenticated && ei != nil && ei.Reason == "MISSING_CALLER" && ei.Domain == "be",
			"%s without be-caller = %s %v, want UNAUTHENTICATED with ErrorInfo MISSING_CALLER/be (P7.2)", name, st.Code(), ei)
	}
}

func (r *Run) recordGRPC(ei *errdetails.ErrorInfo) {
	if ei != nil {
		r.grpcSeen = append(r.grpcSeen, [2]string{ei.Domain, ei.Reason})
	}
}

// CP-RPC-02: a system call's log line carries caller = be-caller (P7.2, P7.4, P18.2).
func caseRPC02(ctx context.Context, r *Run) {
	const id = "CP-RPC-02"
	cc, ok := r.grpcConn(id)
	if !ok {
		return
	}
	defer cc.Close()
	caller := "compconf/rpc02-" + strings.ToLower(infraToken()[:8])
	rid := "compconf-rpc02-" + infraToken()[:10]
	name := sortedKeys(r.methods)[0]
	md := r.methods[name]
	invokeRaw(ctx, cc, name, md, dynamicpb.NewMessage(md.Input()), caller, rid)
	var lines []fakes.LogLine
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline) && len(lines) == 0; time.Sleep(200 * time.Millisecond) {
		lines = r.logs.Find(func(l fakes.LogLine) bool { return l.Str("request_id") == rid })
	}
	if !r.ev.check(id, len(lines) > 0, "no log line with request_id %s after a call to %s (P7.4: the gRPC access log)", rid, name) {
		return
	}
	found := false
	for _, l := range lines {
		if l.Str("msg") == "grpc_request" {
			found = true
			r.ev.check(id, l.Str("caller") == caller, "grpc_request line has caller %q, want %q (P7.2, P18.2)", l.Str("caller"), caller)
			r.ev.check(id, l.Str("rpc.method") != "" && l.Str("rpc.service") != "", "grpc_request line lacks rpc.service / rpc.method")
		}
	}
	r.ev.check(id, found, "no grpc_request line for the call (P18.2)")
}

// CP-RPC-03: a user-facing rpc answers UNAUTHENTICATED before any component code runs, never
// INTERNAL, and the process stays up (P7.3; reason TOKEN_INVALID as ruled for rc.2).
func caseRPC03(ctx context.Context, r *Run) {
	const id = "CP-RPC-03"
	ops := r.grpcFixtureOps(func(o FixtureOp) bool { return o.UserFacing })
	if len(ops) == 0 {
		r.ev.notApplicable(id, "fixtures mark no rpc user_facing")
		return
	}
	cc, ok := r.grpcConn(id)
	if !ok {
		return
	}
	defer cc.Close()
	for _, op := range ops {
		for i := 0; i < 3; i++ {
			st := r.grpcCall(ctx, cc, op, uuidv7(), true)
			ei := errorInfo(st)
			r.ev.check(id, st.Code() == codes.Unauthenticated && ei != nil && ei.Reason == "TOKEN_INVALID",
				"user-facing %s over gRPC = %s %v, want UNAUTHENTICATED / TOKEN_INVALID (P7.3)", op.GRPC, st.Code(), ei)
		}
	}
	running, code, _ := r.main.Running(ctx)
	r.ev.check(id, running, "the component exited (%d) after user-facing calls over gRPC (P7.3)", code)
}

// grpcFixtureOps lists the fixtures' gRPC operations that pass the filter.
func (r *Run) grpcFixtureOps(keep func(FixtureOp) bool) []FixtureOp {
	var out []FixtureOp
	for _, res := range sortedKeys(r.comp.Fixtures.Resources) {
		for _, name := range sortedKeys(r.comp.Fixtures.Resources[res]) {
			if op := r.comp.Fixtures.Resources[res][name]; op.GRPC != "" && keep(op) {
				out = append(out, op)
			}
		}
	}
	return out
}

// bigMessage fills the first string or bytes field of a method's input with n bytes.
func bigMessage(md protoreflect.MethodDescriptor, n int) (*dynamicpb.Message, bool) {
	in := dynamicpb.NewMessage(md.Input())
	fs := md.Input().Fields()
	for i := 0; i < fs.Len(); i++ {
		f := fs.Get(i)
		if f.IsMap() || (f.Kind() != protoreflect.StringKind && f.Kind() != protoreflect.BytesKind) {
			continue
		}
		var v protoreflect.Value
		if f.Kind() == protoreflect.StringKind {
			v = protoreflect.ValueOfString(strings.Repeat("a", n))
		} else {
			v = protoreflect.ValueOfBytes(make([]byte, n))
		}
		if f.IsList() {
			in.Mutable(f).List().Append(v)
		} else {
			in.Set(f, v)
		}
		return in, true
	}
	return nil, false
}

// CP-RPC-04: a 5 MiB request is refused with RESOURCE_EXHAUSTED; a 3 MiB one is not (P7.5:
// the receive limit is 4 MiB).
func caseRPC04(ctx context.Context, r *Run) {
	const id = "CP-RPC-04"
	cc, ok := r.grpcConn(id)
	if !ok {
		return
	}
	defer cc.Close()
	for _, name := range sortedKeys(r.methods) {
		md := r.methods[name]
		big, ok := bigMessage(md, 5<<20)
		if !ok {
			continue
		}
		st := invokeRaw(ctx, cc, name, md, big, "compconf/suite", "compconf-rpc04")
		r.ev.check(id, st.Code() == codes.ResourceExhausted, "%s with a 5 MiB request = %s, want RESOURCE_EXHAUSTED (P7.5)", name, st.Code())
		mid, _ := bigMessage(md, 3<<20)
		st = invokeRaw(ctx, cc, name, md, mid, "compconf/suite", "compconf-rpc04")
		r.ev.check(id, st.Code() != codes.ResourceExhausted && st.Code() != codes.Unavailable,
			"%s with a 3 MiB request = %s: the limit is below 4 MiB (P7.5)", name, st.Code())
		return
	}
	r.ev.notApplicable(id, "no method takes a string or bytes field to fill")
}

// CP-RPC-05: past MaxConnectionAge (the suite sets GRPC_MAX_CONNECTION_AGE=10s) the server
// sends GOAWAY: the client dials again and no call fails meanwhile (P7.5).
func caseRPC05(ctx context.Context, r *Run) {
	const id = "CP-RPC-05"
	if !r.ev.check(id, r.comp.HasConfigKey("GRPC_MAX_CONNECTION_AGE"), "configSchema does not declare GRPC_MAX_CONNECTION_AGE (P2.8, P7.5)") {
		return
	}
	var dials atomic.Int64
	dialer := func(ctx context.Context, addr string) (net.Conn, error) {
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
	cc, ok := r.grpcConn(id, grpc.WithContextDialer(dialer))
	if !ok {
		return
	}
	defer cc.Close()
	name := sortedKeys(r.methods)[0]
	md := r.methods[name]
	failed := 0
	for end := time.Now().Add(26 * time.Second); time.Now().Before(end); time.Sleep(300 * time.Millisecond) {
		st := invokeRaw(ctx, cc, name, md, dynamicpb.NewMessage(md.Input()), "compconf/suite", "compconf-rpc05")
		if st.Code() == codes.Unavailable || st.Code() == codes.Internal {
			failed++
			r.ev.fail(id, "a call failed with %s around a connection rotation: %s (P7.5: a GOAWAY never cuts an admitted call)", st.Code(), st.Message())
		}
	}
	r.ev.check(id, dials.Load() >= 2, "one TCP connection for 26 s with GRPC_MAX_CONNECTION_AGE=10s: no GOAWAY (P7.5)")
	r.ev.pass(id, fmt.Sprintf("%d connections dialled, %d failed calls", dials.Load(), failed))
}

// maxItems is the (be.v1.max_items) option of a field, 500 without it (P7.10).
func maxItems(f protoreflect.FieldDescriptor) int {
	n := 500
	f.Options().ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.FullName() == "be.v1.max_items" {
			n = int(v.Int())
		}
		return true
	})
	return n
}

// CP-RPC-06: a repeated ID field above its limit answers INVALID_ARGUMENT BATCH_TOO_LARGE with
// metadata field, max, got and a BadRequest; exactly the limit is accepted (P7.10).
func caseRPC06(ctx context.Context, r *Run) {
	const id = "CP-RPC-06"
	cc, ok := r.grpcConn(id)
	if !ok {
		return
	}
	defer cc.Close()
	n := 0
	for _, name := range sortedKeys(r.methods) {
		md := r.methods[name]
		fs := md.Input().Fields()
		for i := 0; i < fs.Len(); i++ {
			f := fs.Get(i)
			if !f.IsList() || f.IsMap() || f.Kind() != protoreflect.StringKind {
				continue
			}
			n++
			r.checkBatchLimit(ctx, id, cc, name, md, f)
		}
	}
	if n == 0 {
		r.ev.notApplicable(id, "no method takes a repeated string field")
	}
}

func (r *Run) checkBatchLimit(ctx context.Context, id string, cc *grpc.ClientConn, name string, md protoreflect.MethodDescriptor, f protoreflect.FieldDescriptor) {
	limit := maxItems(f)
	fill := func(k int) *dynamicpb.Message {
		in := dynamicpb.NewMessage(md.Input())
		l := in.Mutable(f).List()
		for i := 0; i < k; i++ {
			l.Append(protoreflect.ValueOfString(uuidv7()))
		}
		return in
	}
	st := invokeRaw(ctx, cc, name, md, fill(limit+1), "compconf/suite", "compconf-rpc06")
	ei := errorInfo(st)
	r.recordGRPC(ei)
	ok := st.Code() == codes.InvalidArgument && ei != nil && ei.Reason == "BATCH_TOO_LARGE" && ei.Domain == "be"
	if !r.ev.check(id, ok, "%s with %d %s (limit %d) = %s %v, want INVALID_ARGUMENT BATCH_TOO_LARGE (P7.10)", name, limit+1, f.Name(), limit, st.Code(), ei) {
		return
	}
	r.ev.check(id, ei.Metadata["field"] == string(f.Name()) && ei.Metadata["max"] == strconv.Itoa(limit) && ei.Metadata["got"] == strconv.Itoa(limit+1),
		"%s BATCH_TOO_LARGE metadata %v, want field=%s max=%d got=%d (P7.10)", name, ei.Metadata, f.Name(), limit, limit+1)
	hasBR := false
	for _, d := range st.Details() {
		if _, ok := d.(*errdetails.BadRequest); ok {
			hasBR = true
		}
	}
	r.ev.check(id, hasBR, "%s BATCH_TOO_LARGE without a BadRequest detail (P7.10)", name)
	st = invokeRaw(ctx, cc, name, md, fill(limit), "compconf/suite", "compconf-rpc06")
	ei = errorInfo(st)
	r.ev.check(id, ei == nil || ei.Reason != "BATCH_TOO_LARGE", "%s with exactly %d %s answered BATCH_TOO_LARGE (P7.10)", name, limit, f.Name())
}
