package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func server(t *testing.T, h http.HandlerFunc) string {
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s.URL
}

func client() *http.Client {
	return &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 1024}}
}

func TestTheRateIsHeld(t *testing.T) {
	url := server(t, func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{}`)) })
	s := summarize(Plan{Duration: 2 * time.Second}, Run(context.Background(), client(),
		Plan{Target: url, Op: Op{Kind: "echo", Bytes: 10}, Rate: 200, Warmup: 500 * time.Millisecond, Duration: 2 * time.Second}))
	if s.Sent < 395 || s.Sent > 405 || s.OK != s.Sent {
		t.Fatalf("sent %d, ok %d; want 400 at 200/s for 2 s", s.Sent, s.OK)
	}
}

// TestAStallShowsInEveryRequestItDelayed: the server stalls for 500 ms; every request due in that time must report
// the wait, as a user would see it (a closed loop would report one slow request and quietly send fewer).
func TestAStallShowsInEveryRequestItDelayed(t *testing.T) {
	start := time.Now()
	stallFrom, stallTo := start.Add(time.Second), start.Add(1500*time.Millisecond)
	var mu sync.Mutex // one request at a time, like a frozen process
	url := server(t, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if now := time.Now(); now.After(stallFrom) && now.Before(stallTo) {
			time.Sleep(time.Until(stallTo))
		}
		w.Write([]byte(`{}`))
	})
	samples := Run(context.Background(), client(), Plan{Target: url, Op: Op{Kind: "echo"}, Rate: 100, Duration: 3 * time.Second})
	slow := 0
	var worst time.Duration
	for _, s := range samples {
		if s.Latency >= 100*time.Millisecond {
			slow++
		}
		worst = max(worst, s.Latency)
	}
	if slow < 35 || worst < 400*time.Millisecond {
		t.Fatalf("%d requests waited 100 ms or more, the worst %v; want about 50 and about 500 ms", slow, worst)
	}
}

func TestTheHopAndTheWritesAreKept(t *testing.T) {
	url := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/probe/call" {
			w.Write([]byte(`{"ok":true,"elapsed_us":1500}`))
			return
		}
		w.WriteHeader(201)
		w.Write([]byte(`{"id":"0190f3a1-7b2c-7def-8a01-23456789abcd"}`))
	})
	p := Plan{Target: url, Op: Op{Kind: "call", Surface: "grpc", Inner: "echo"}, Rate: 50, Duration: time.Second}
	s := summarize(p, Run(context.Background(), client(), p))
	if s.Hop == nil || s.Hop.P50 != 1.5 {
		t.Fatalf("hop %+v, want 1.5 ms", s.Hop)
	}
	p.Op = Op{Kind: "write", Bytes: 10}
	s = summarize(p, Run(context.Background(), client(), p))
	if len(s.Writes) != s.OK || s.Writes[0].ID == "" || s.Writes[0].DueNS == 0 {
		t.Fatalf("writes %+v", s.Writes)
	}
}

// TestTheRampStopsAtCapacity: a server that handles 50 requests per second (one at a time, 20 ms each).
func TestTheRampStopsAtCapacity(t *testing.T) {
	var mu sync.Mutex
	url := server(t, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		time.Sleep(20 * time.Millisecond)
		mu.Unlock()
		w.Write([]byte(`{}`))
	})
	r := ramp(context.Background(), client(), Plan{Target: url, Op: Op{Kind: "echo"}},
		Ramp{Start: 20, Step: 20, Max: 200, StepTime: 2 * time.Second, P99Limit: 300 * time.Millisecond, MaxErrorRate: 0.01})
	if r.StoppedBy != "p99" || r.Sustained < 20 || r.Sustained > 60 {
		t.Fatalf("stopped by %s at a sustained %v/s; want p99 near 50/s (%d steps)", r.StoppedBy, r.Sustained, len(r.Steps))
	}
	// with steps shorter than the latency limit, saturation is what stops it
	r = ramp(context.Background(), client(), Plan{Target: url, Op: Op{Kind: "echo"}},
		Ramp{Start: 20, Factor: 1.5, Max: 400, StepTime: 2 * time.Second, P99Limit: time.Minute, MaxErrorRate: 0.01,
			MinThroughput: 0.9})
	if r.StoppedBy != "saturated" || r.Sustained < 20 || r.Sustained > 60 {
		t.Fatalf("stopped by %s at a sustained %v/s; want p99 near 50/s (%d steps)", r.StoppedBy, r.Sustained, len(r.Steps))
	}
}
