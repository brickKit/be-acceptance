package fakes

import (
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// Peer is fake-peer: it answers a dependency's gRPC methods from the dependency's proto
// descriptors and its user-plane HTTP operations, with canned answers; it can fail, hang or
// slow down any of them, and it records what it receives (metadata, deadlines, connections,
// headers). This is how the outbound cases observe the component.
type Peer struct {
	ID      string
	methods map[string]protoreflect.MethodDescriptor
	mu      sync.Mutex
	answers map[string]PeerAnswer // rpc full name or "<METHOD> <path>"
	calls   []Call
	http    []HTTPCall
	conns   atomic.Int64
	release chan struct{}
	grpcSrv *grpc.Server
	grpcLn  net.Listener
	httpSrv *Server
}

// PeerAnswer is how the peer answers one call.
type PeerAnswer struct {
	Response []byte     // JSON (protojson for gRPC); empty = an empty message / {}
	Code     codes.Code // non-OK: fail with this code and an ErrorInfo
	Reason   string
	Domain   string
	Hang     bool          // never answer until Release or the caller's deadline
	Delay    time.Duration // answer after this delay
}

// Call is one recorded gRPC call.
type Call struct {
	Method   string
	Metadata metadata.MD
	Deadline time.Duration // remaining at arrival; 0 = the caller set none
	Remote   string
	Request  []byte // protojson
	At       time.Time
}

// HTTPCall is one recorded HTTP request.
type HTTPCall struct {
	Route  string
	Path   string
	Header http.Header
	At     time.Time
}

// NewPeer compiles the dependency's protos (contracts holds them at their package paths;
// imports resolve be/v1/limits.proto and the like).
func NewPeer(id string, contracts fs.FS, imports ...fs.FS) (*Peer, error) {
	methods, err := CompileProtos(contracts, imports...)
	if err != nil {
		return nil, fmt.Errorf("fake-peer %s: %w", id, err)
	}
	p := &Peer{ID: id, methods: methods, answers: map[string]PeerAnswer{}, release: make(chan struct{})}
	p.httpSrv = NewServer(http.HandlerFunc(p.serveHTTP))
	return p, nil
}

// Method returns a method descriptor by full name (pkg.Service/Method).
func (p *Peer) Method(name string) (protoreflect.MethodDescriptor, bool) {
	m, ok := p.methods[name]
	return m, ok
}

// SetAnswer sets the answer of a gRPC method.
func (p *Peer) SetAnswer(method string, a PeerAnswer) {
	p.mu.Lock()
	p.answers[method] = a
	p.mu.Unlock()
}

// Answer returns the current answer of a method or route.
func (p *Peer) Answer(method string) PeerAnswer {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.answers[method]
}

// SetHTTPAnswer sets the answer of an HTTP operation "<METHOD> <path>" ({param} placeholders).
func (p *Peer) SetHTTPAnswer(route string, a PeerAnswer) { p.SetAnswer(route, a) }

// Release answers every hung call (with its canned response) and lets later hangs proceed.
func (p *Peer) Release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	close(p.release)
	p.release = make(chan struct{})
}

// Start serves gRPC on grpcAddr and HTTP on httpAddr.
func (p *Peer) Start(grpcAddr, httpAddr string) error {
	ln, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		return err
	}
	p.grpcLn = &countingListener{Listener: ln, n: &p.conns}
	p.grpcSrv = grpc.NewServer(grpc.UnknownServiceHandler(p.handle))
	go func() { _ = p.grpcSrv.Serve(p.grpcLn) }()
	return p.httpSrv.Start(httpAddr)
}

// Stop stops both servers.
func (p *Peer) Stop() {
	if p.grpcSrv != nil {
		p.grpcSrv.Stop()
	}
	p.httpSrv.Stop()
}

// GRPCAddr is host:port of the gRPC listener.
func (p *Peer) GRPCAddr() string { return p.grpcLn.Addr().String() }

// GRPCPort is the gRPC port.
func (p *Peer) GRPCPort() int { return p.grpcLn.Addr().(*net.TCPAddr).Port }

// HTTPServer is the HTTP side.
func (p *Peer) HTTPServer() *Server { return p.httpSrv }

// HTTPURL is the HTTP base on 127.0.0.1.
func (p *Peer) HTTPURL() string { return p.httpSrv.URL("127.0.0.1") }

// Connections counts accepted gRPC TCP connections.
func (p *Peer) Connections() int64 { return p.conns.Load() }

// Calls returns the recorded calls of one method ("" = all).
func (p *Peer) Calls(method string) []Call {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []Call
	for _, c := range p.calls {
		if method == "" || c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// HTTPCalls returns the recorded HTTP requests.
func (p *Peer) HTTPCalls() []HTTPCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]HTTPCall(nil), p.http...)
}

func (p *Peer) handle(_ any, stream grpc.ServerStream) error {
	full, _ := grpc.MethodFromServerStream(stream)
	name := full[1:]
	md, ok := p.methods[name]
	if !ok {
		return status.Errorf(codes.Unimplemented, "fake-peer %s has no method %s", p.ID, name)
	}
	in := dynamicpb.NewMessage(md.Input())
	if err := stream.RecvMsg(in); err != nil {
		return err
	}
	p.record(stream, name, in)
	p.mu.Lock()
	a, rel := p.answers[name], p.release
	p.mu.Unlock()
	if err := wait(stream, a, rel); err != nil {
		return err
	}
	if a.Code != codes.OK {
		return answerError(a)
	}
	out := dynamicpb.NewMessage(md.Output())
	if len(a.Response) > 0 {
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(a.Response, out); err != nil {
			return status.Errorf(codes.Internal, "fake-peer canned answer for %s: %v", name, err)
		}
	}
	return stream.SendMsg(out)
}

func (p *Peer) record(stream grpc.ServerStream, name string, in *dynamicpb.Message) {
	c := Call{Method: name, At: time.Now()}
	c.Metadata, _ = metadata.FromIncomingContext(stream.Context())
	if dl, ok := stream.Context().Deadline(); ok {
		c.Deadline = time.Until(dl)
	}
	if pr, ok := peer.FromContext(stream.Context()); ok {
		c.Remote = pr.Addr.String()
	}
	c.Request, _ = protojson.Marshal(in)
	p.mu.Lock()
	p.calls = append(p.calls, c)
	p.mu.Unlock()
}

func wait(stream grpc.ServerStream, a PeerAnswer, rel chan struct{}) error {
	var timer <-chan time.Time
	switch {
	case a.Hang:
	case a.Delay > 0:
		timer = time.After(a.Delay)
	default:
		return nil
	}
	select {
	case <-stream.Context().Done():
		return status.FromContextError(stream.Context().Err()).Err()
	case <-rel:
	case <-timer:
	}
	return nil
}

func answerError(a PeerAnswer) error {
	st := status.New(a.Code, "fake-peer: "+a.Reason)
	if a.Reason != "" {
		if d, err := st.WithDetails(&errdetails.ErrorInfo{Reason: a.Reason, Domain: a.Domain}); err == nil {
			st = d
		}
	}
	return st.Err()
}

type countingListener struct {
	net.Listener
	n *atomic.Int64
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.n.Add(1)
	}
	return c, err
}
