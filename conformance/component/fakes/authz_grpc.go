package fakes

import (
	"context"
	"encoding/json"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	authzv2 "github.com/brickKit/contract-infra-authz/v2/gen/go/infra/authz/v2"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AuthzCall is one recorded gRPC call to fake-authz.
type AuthzCall struct {
	Method, Caller string
	At             time.Time
}

// authzGRPC is the gRPC side of fake-authz: infra.authz.v2.AuthzProvider. Every call needs
// be-caller; an rpc of an optional capability the fake does not offer answers UNIMPLEMENTED
// with ErrorInfo CAPABILITY_UNAVAILABLE (the rules at the top of provider.proto).
type authzGRPC struct {
	authzv2.UnimplementedAuthzProviderServer
	f     *Authz
	mu    sync.Mutex
	addr  string
	srv   *grpc.Server
	calls []AuthzCall
	idem  map[string]string // WriteTuples idempotency key -> revision
}

// StartGRPC serves the provider's gRPC service on addr ("127.0.0.1:0" picks a port; a
// restart passes the same address).
func (f *Authz) StartGRPC(addr string) error {
	if f.grpc == nil {
		f.grpc = &authzGRPC{f: f, idem: map[string]string{}}
	}
	g := f.grpc
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := grpc.NewServer(grpc.UnaryInterceptor(g.intercept))
	authzv2.RegisterAuthzProviderServer(srv, g)
	g.mu.Lock()
	g.addr, g.srv = ln.Addr().String(), srv
	g.mu.Unlock()
	go func() { _ = srv.Serve(ln) }()
	return nil
}

// StopGRPC stops the gRPC side: callers see UNAVAILABLE.
func (f *Authz) StopGRPC() {
	if f.grpc == nil {
		return
	}
	f.grpc.mu.Lock()
	srv := f.grpc.srv
	f.grpc.srv = nil
	f.grpc.mu.Unlock()
	if srv != nil {
		srv.Stop()
	}
}

// RestartGRPC listens again on the first address.
func (f *Authz) RestartGRPC() error {
	if f.grpc == nil {
		return nil
	}
	var err error
	for i := 0; i < 50; i++ {
		if err = f.StartGRPC(f.GRPCAddr()); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return err
}

// GRPCAddr is host:port of the gRPC listener.
func (f *Authz) GRPCAddr() string {
	if f.grpc == nil {
		return ""
	}
	f.grpc.mu.Lock()
	defer f.grpc.mu.Unlock()
	return f.grpc.addr
}

// GRPCPort is the gRPC port.
func (f *Authz) GRPCPort() int {
	_, p, _ := net.SplitHostPort(f.GRPCAddr())
	n, _ := strconv.Atoi(p)
	return n
}

// GRPCCalls lists the recorded calls of one method ("" = all).
func (f *Authz) GRPCCalls(method string) []AuthzCall {
	if f.grpc == nil {
		return nil
	}
	f.grpc.mu.Lock()
	defer f.grpc.mu.Unlock()
	var out []AuthzCall
	for _, c := range f.grpc.calls {
		if method == "" || c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func (g *authzGRPC) intercept(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	caller := ""
	if v := md.Get("be-caller"); len(v) > 0 {
		caller = v[0]
	}
	method := info.FullMethod[strings.LastIndex(info.FullMethod, "/")+1:]
	g.mu.Lock()
	g.calls = append(g.calls, AuthzCall{Method: method, Caller: caller, At: time.Now()})
	g.mu.Unlock()
	if caller == "" {
		return nil, errInfo(codes.Unauthenticated, "MISSING_CALLER", nil)
	}
	return h(ctx, req)
}

func errInfo(code codes.Code, reason string, md map[string]string) error {
	st := status.New(code, "fake-authz: "+reason)
	if d, err := st.WithDetails(&errdetails.ErrorInfo{Reason: reason, Domain: "be", Metadata: md}); err == nil {
		st = d
	}
	return st.Err()
}

// need answers CAPABILITY_UNAVAILABLE unless one of the capabilities is true.
func (g *authzGRPC) need(caps ...string) error {
	g.f.mu.Lock()
	defer g.f.mu.Unlock()
	for _, c := range caps {
		if v, _ := g.f.caps[c].(bool); v {
			return nil
		}
	}
	return errInfo(codes.Unimplemented, "CAPABILITY_UNAVAILABLE", map[string]string{"capability": caps[0]})
}

func (g *authzGRPC) GetBundle(_ context.Context, in *authzv2.GetBundleRequest) (*authzv2.GetBundleResponse, error) {
	b, etag := g.f.bundle()
	if in.IfNoneMatch == etag {
		return &authzv2.GetBundleResponse{Etag: etag, NotModified: true}, nil
	}
	raw, _ := json.Marshal(b)
	return &authzv2.GetBundleResponse{Json: raw, Etag: etag}, nil
}

func (g *authzGRPC) GetCatalog(context.Context, *authzv2.GetCatalogRequest) (*authzv2.GetCatalogResponse, error) {
	b, _ := g.f.bundle()
	return &authzv2.GetCatalogResponse{Digest: b["catalog_digest"].(string)}, nil
}

func (g *authzGRPC) ResolveClaims(_ context.Context, in *authzv2.ResolveClaimsRequest) (*authzv2.ResolveClaimsResponse, error) {
	return &authzv2.ResolveClaimsResponse{Roles: []string{}, Revision: strconv.FormatInt(g.f.Revision(), 10)}, nil
}

func (g *authzGRPC) BatchGetRoles(_ context.Context, in *authzv2.BatchGetRolesRequest) (*authzv2.BatchGetRolesResponse, error) {
	g.f.mu.Lock()
	defer g.f.mu.Unlock()
	out := &authzv2.BatchGetRolesResponse{}
	for _, code := range in.Codes {
		if keys, ok := g.f.roles[code]; ok {
			out.Roles = append(out.Roles, &authzv2.Role{Code: code, Name: code, Keys: keys})
		}
	}
	return out, nil
}

func (g *authzGRPC) ReadChanges(_ context.Context, in *authzv2.ReadChangesRequest) (*authzv2.ReadChangesResponse, error) {
	if err := g.need("sharing", "relation_sync"); err != nil {
		return nil, err
	}
	after, _ := strconv.ParseInt(in.After, 10, 64)
	cs, next, wm := g.f.readChanges(in.Types, after, int(in.Limit))
	out := &authzv2.ReadChangesResponse{Next: next, Watermark: wm}
	for _, c := range cs {
		op := authzv2.ChangeOp_CHANGE_OP_UPSERT
		if c.op == "delete" {
			op = authzv2.ChangeOp_CHANGE_OP_DELETE
		}
		out.Changes = append(out.Changes, &authzv2.Change{Revision: strconv.FormatInt(c.rev, 10), Op: op, Tuple: toProto(c.tuple)})
	}
	return out, nil
}

func (g *authzGRPC) ReadTuples(_ context.Context, in *authzv2.ReadTuplesRequest) (*authzv2.ReadTuplesResponse, error) {
	if err := g.need("sharing", "relation_sync"); err != nil {
		return nil, err
	}
	out := &authzv2.ReadTuplesResponse{Revision: strconv.FormatInt(g.f.Revision(), 10)}
	for _, t := range g.f.Tuples(in.Type) {
		out.Tuples = append(out.Tuples, toProto(t))
	}
	return out, nil
}

func (g *authzGRPC) WriteTuples(_ context.Context, in *authzv2.WriteTuplesRequest) (*authzv2.WriteTuplesResponse, error) {
	if err := g.need("sharing"); err != nil {
		return nil, err
	}
	g.mu.Lock()
	rev, seen := g.idem[in.IdempotencyKey]
	g.mu.Unlock()
	if seen && in.IdempotencyKey != "" {
		return &authzv2.WriteTuplesResponse{Revision: rev}, nil
	}
	rev = g.f.applyTuples(fromProto(in.Writes), fromProto(in.Deletes))
	if in.IdempotencyKey != "" {
		g.mu.Lock()
		g.idem[in.IdempotencyKey] = rev
		g.mu.Unlock()
	}
	return &authzv2.WriteTuplesResponse{Revision: rev}, nil
}

// Check answers from the direct tuples only (the fake does no derivation).
func (g *authzGRPC) Check(_ context.Context, in *authzv2.CheckRequest) (*authzv2.CheckResponse, error) {
	if err := g.need("check"); err != nil {
		return nil, err
	}
	return &authzv2.CheckResponse{Allowed: g.f.holds(in.Principal, in.Item), Revision: strconv.FormatInt(g.f.Revision(), 10)}, nil
}

func (g *authzGRPC) BatchCheck(_ context.Context, in *authzv2.BatchCheckRequest) (*authzv2.BatchCheckResponse, error) {
	if err := g.need("check"); err != nil {
		return nil, err
	}
	out := &authzv2.BatchCheckResponse{}
	for _, it := range in.Items {
		out.Results = append(out.Results, &authzv2.CheckResponse{Allowed: g.f.holds(in.Principal, it), Revision: strconv.FormatInt(g.f.Revision(), 10)})
	}
	return out, nil
}

func (g *authzGRPC) ListObjects(context.Context, *authzv2.ListObjectsRequest) (*authzv2.ListObjectsResponse, error) {
	if err := g.need("graph"); err != nil {
		return nil, err
	}
	return &authzv2.ListObjectsResponse{Revision: strconv.FormatInt(g.f.Revision(), 10)}, nil
}

// Explain gives no facts: the fake keeps no catalogue of keys to explain.
func (g *authzGRPC) Explain(context.Context, *authzv2.ExplainRequest) (*authzv2.ExplainResponse, error) {
	return &authzv2.ExplainResponse{}, nil
}

func (g *authzGRPC) CreateDelegation(context.Context, *authzv2.CreateDelegationRequest) (*authzv2.CreateDelegationResponse, error) {
	return nil, g.need("delegation")
}

func (g *authzGRPC) RevokeDelegation(context.Context, *authzv2.RevokeDelegationRequest) (*authzv2.RevokeDelegationResponse, error) {
	return nil, g.need("delegation")
}

func (g *authzGRPC) BatchGetDelegations(context.Context, *authzv2.BatchGetDelegationsRequest) (*authzv2.BatchGetDelegationsResponse, error) {
	return nil, g.need("delegation")
}

// holds: a tuple of the item's object, one of its relations, and a subject of the principal.
func (f *Authz) holds(p *authzv2.Principal, it *authzv2.CheckItem) bool {
	if p == nil || it == nil || it.Object == nil {
		return false
	}
	subjects := map[string]bool{"user:" + p.Sub: true}
	for _, r := range p.Roles {
		subjects["role:"+r] = true
	}
	if d := p.DeptPath; strings.HasPrefix(d, "/") && strings.HasSuffix(d, "/") {
		subjects["dept:"+d] = true
		segs := strings.Split(strings.Trim(d, "/"), "/")
		path := "/"
		subjects["dept_tree:/"] = true
		for _, s := range segs {
			path += s + "/"
			subjects["dept_tree:"+path] = true
		}
	}
	now := time.Now()
	for _, t := range f.Tuples(it.Object.Type) {
		if t.ID != it.Object.Id || !subjects[t.Subject] || (t.ExpiresAt != nil && !t.ExpiresAt.After(now)) {
			continue
		}
		for _, rel := range it.Relations {
			if rel == t.Relation {
				return true
			}
		}
	}
	return false
}

func toProto(t TupleRec) *authzv2.Tuple {
	pt := &authzv2.Tuple{Object: &authzv2.ObjectRef{Type: t.Type, Id: t.ID}, Relation: t.Relation, Subject: t.Subject}
	if t.ExpiresAt != nil {
		pt.ExpiresAt = timestamppb.New(*t.ExpiresAt)
	}
	return pt
}

func fromProto(ts []*authzv2.Tuple) []TupleRec {
	out := make([]TupleRec, 0, len(ts))
	for _, t := range ts {
		r := TupleRec{Relation: t.Relation, Subject: t.Subject}
		if t.Object != nil {
			r.Type, r.ID = t.Object.Type, t.Object.Id
		}
		if t.ExpiresAt != nil {
			at := t.ExpiresAt.AsTime()
			r.ExpiresAt = &at
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out
}
