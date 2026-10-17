package probe

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// The system surface: /test.probe.v1.Probe/Echo (BytesValue) and /Work (UInt32Value, milliseconds of CPU). Written
// by hand so the probe needs no generated code; the Python probe registers the same methods.
const (
	EchoMethod = "/test.probe.v1.Probe/Echo"
	WorkMethod = "/test.probe.v1.Probe/Work"
)

type server interface {
	Echo(context.Context, *wrapperspb.BytesValue) (*wrapperspb.BytesValue, error)
	Work(context.Context, *wrapperspb.UInt32Value) (*wrapperspb.UInt32Value, error)
}

func handler[In any, Out any](full string, call func(server, context.Context, *In) (*Out, error)) grpc.MethodHandler {
	return func(srv any, ctx context.Context, dec func(any) error, ic grpc.UnaryServerInterceptor) (any, error) {
		in := new(In)
		if err := dec(in); err != nil {
			return nil, err
		}
		h := func(ctx context.Context, req any) (any, error) { return call(srv.(server), ctx, req.(*In)) }
		if ic == nil {
			return h(ctx, in)
		}
		return ic(ctx, in, &grpc.UnaryServerInfo{Server: srv, FullMethod: full}, h)
	}
}

var desc = grpc.ServiceDesc{ServiceName: "test.probe.v1.Probe", HandlerType: (*server)(nil), Methods: []grpc.MethodDesc{
	{MethodName: "Echo", Handler: handler(EchoMethod, func(s server, ctx context.Context, in *wrapperspb.BytesValue) (*wrapperspb.BytesValue, error) {
		return s.Echo(ctx, in)
	})},
	{MethodName: "Work", Handler: handler(WorkMethod, func(s server, ctx context.Context, in *wrapperspb.UInt32Value) (*wrapperspb.UInt32Value, error) {
		return s.Work(ctx, in)
	})},
}}
