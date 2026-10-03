// Package fakes holds the suite's in-process fake services: the identity provider (fake-iam),
// the authorization provider (fake-authz), the dependency peer (fake-peer, gRPC and HTTP) and
// the telemetry observer (OTLP receiver and log capture). Each can be stopped and started
// again on the same port, which is how the suite simulates a provider that goes away.
package fakes

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Server is an HTTP server that can be stopped and restarted on the same port.
type Server struct {
	handler http.Handler
	mu      sync.Mutex
	addr    string // the resolved listen address after the first Start
	srv     *http.Server
}

// NewServer wraps a handler.
func NewServer(h http.Handler) *Server { return &Server{handler: h} }

// Start listens on addr ("0.0.0.0:0" picks a port) and serves in the background.
func (s *Server) Start(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addr = ln.Addr().String()
	s.srv = &http.Server{Handler: s.handler, ReadHeaderTimeout: 10 * time.Second}
	go func(srv *http.Server) { _ = srv.Serve(ln) }(s.srv)
	return nil
}

// Stop closes the listener and every open connection: callers see connection refused.
func (s *Server) Stop() {
	s.mu.Lock()
	srv := s.srv
	s.srv = nil
	s.mu.Unlock()
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_ = srv.Shutdown(ctx)
		_ = srv.Close()
	}
}

// Restart listens again on the port of the first Start.
func (s *Server) Restart() error {
	s.mu.Lock()
	addr := s.addr
	s.mu.Unlock()
	if addr == "" {
		return fmt.Errorf("server never started")
	}
	var err error
	for i := 0; i < 50; i++ { // the port may linger briefly after Close
		if err = s.Start(addr); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return err
}

// Running reports whether the server currently listens.
func (s *Server) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.srv != nil
}

// Port is the listening port.
func (s *Server) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, p, _ := net.SplitHostPort(s.addr)
	n, _ := strconv.Atoi(p)
	return n
}

// URL is http://<host>:<port> for the given host name.
func (s *Server) URL(host string) string {
	return "http://" + net.JoinHostPort(host, strconv.Itoa(s.Port()))
}
