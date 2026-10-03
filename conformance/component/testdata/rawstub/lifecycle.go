package main

import (
	"context"

	"google.golang.org/grpc"
)

// The lifecycle resource contract (P16.4, P16.8): every operation of be-protocol
// openapi/resource-lifecycle.yaml and gRPC be.lifecycle.v1.Lifecycle is mounted and guarded; this
// runtime has no cold store and keeps no units, so each answers 501 CAPABILITY_UNAVAILABLE.

const lifecyclePrefix = userPrefix + "/_lifecycle"

func lifecycleRoutes() []*route {
	key := func(k string) string { return "conformance.rawstub.lifecycle." + k }
	ops := []struct{ method, path, key string }{
		{"GET", "/units", "read"},
		{"POST", "/units/{table}/{unit}:thaw", "thaw"},
		{"GET", "/verify", "read"},
		{"POST", "/exports", "read"},
		{"GET", "/exports/{job_id}", "read"},
		{"GET", "/holds", "admin"},
		{"POST", "/holds", "admin"},
		{"DELETE", "/holds/{hold_id}", "admin"},
		{"POST", "/erasures", "admin"},
		{"GET", "/erasures/{request_id}", "admin"},
		{"GET", "/destructions", "admin"},
		{"POST", "/destructions/{destruction_id}:approve", "admin"},
	}
	var rs []*route
	for _, o := range ops {
		rs = append(rs, &route{method: o.method, pattern: lifecyclePrefix + o.path, guard: key(o.key), handle: (*App).lifecycleUnavailable})
	}
	return rs
}

func (a *App) lifecycleUnavailable(*reqCtx) error {
	return beErr("CAPABILITY_UNAVAILABLE", map[string]string{"capability": "lifecycle.cold_store"})
}

var lifecycleMethods = []string{"ListUnits", "ThawUnit", "Verify", "CreateExport", "GetExport", "ListHolds",
	"PlaceHold", "ReleaseHold", "RequestErasure", "GetErasure", "ListDestructions", "ApproveDestruction"}

// registerLifecycle mounts be.lifecycle.v1.Lifecycle on the gRPC server.
func registerLifecycle(s *grpc.Server, a *App) {
	const svc = "be.lifecycle.v1.Lifecycle"
	desc := grpc.ServiceDesc{ServiceName: svc, HandlerType: (*any)(nil), Metadata: "be/lifecycle/v1/lifecycle.proto"}
	for _, m := range lifecycleMethods {
		m := m
		desc.Methods = append(desc.Methods, grpc.MethodDesc{
			MethodName: m,
			Handler: func(srv any, ctx context.Context, dec func(any) error, ic grpc.UnaryServerInterceptor) (any, error) {
				in := &rawMsg{}
				if err := dec(in); err != nil {
					return nil, err
				}
				info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/" + svc + "/" + m}
				return ic(ctx, in, info, func(context.Context, any) (any, error) {
					return nil, a.lifecycleUnavailable(nil)
				})
			},
		})
	}
	s.RegisterService(&desc, a)
}
