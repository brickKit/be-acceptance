package main

import (
	"errors"
	"testing"
	"time"
)

// TestAnOutageShowsInItsSeconds: the second of a run in which the target was down reports its failures by reason (one
// key, not one per URL), the seconds around it are clean, and a confirmation left processing is counted either way.
func TestAnOutageShowsInItsSeconds(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	refused := errors.New(`Post "http://127.0.0.1:18084/api/sales/orders/SO-1/confirm": dial tcp 127.0.0.1:18084: connect: connection refused`)
	refused2 := errors.New(`Post "http://127.0.0.1:18084/api/sales/orders/SO-2/confirm": dial tcp 127.0.0.1:18084: connect: connection refused`)
	ms := time.Millisecond
	got := series([]Sample{
		{Due: t0, Latency: 20 * ms},
		{Due: t0.Add(400 * ms), Latency: 30 * ms, Outcome: "processing"},
		{Due: t0.Add(time.Second), Latency: 5 * ms, Error: "confirm: " + refused.Error()},
		{Due: t0.Add(1500 * ms), Latency: 6 * ms, Error: "confirm: " + refused2.Error()},
		{Due: t0.Add(3 * time.Second), Latency: 25 * ms},
	})
	if len(got) != 3 {
		t.Fatalf("got %d seconds, want 3 (the empty second is left out): %+v", len(got), got)
	}
	if got[0].Unix != t0.Unix() || got[0].Sent != 2 || got[0].OK != 2 || got[0].Outcomes["processing"] != 1 || got[0].Max != 30 {
		t.Fatalf("first second: %+v", got[0])
	}
	if got[1].OK != 0 || got[1].Sent != 2 || len(got[1].Errors) != 1 {
		t.Fatalf("the outage second should hold two failures under one reason: %+v", got[1])
	}
	if got[2].Unix != t0.Unix()+3 || got[2].OK != 1 || got[2].Errors != nil {
		t.Fatalf("after the outage: %+v", got[2])
	}
}
