package fakes

import (
	"io"
	"net/http"
	"testing"
)

func TestServerStopsAndRestartsOnSamePort(t *testing.T) {
	s := NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	if err := s.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	get := func() error {
		resp, err := http.Get(s.URL("127.0.0.1") + "/")
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	if err := get(); err != nil {
		t.Fatalf("running server: %v", err)
	}
	port := s.Port()
	s.Stop()
	if err := get(); err == nil {
		t.Fatal("stopped server still answers")
	}
	if err := s.Restart(); err != nil {
		t.Fatal(err)
	}
	if s.Port() != port {
		t.Fatalf("port changed %d -> %d", port, s.Port())
	}
	if err := get(); err != nil {
		t.Fatalf("restarted server: %v", err)
	}
}
