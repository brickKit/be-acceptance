// Package main is the experiment's load driver for the probes (step 4, T4.5): an open loop at a fixed rate, each
// request's latency measured from the moment it was due (so a stall shows in every request it delayed, not only in
// the one that met it: no coordinated omission), the hop the calling probe measured itself, and the identities of
// writes for completion times taken afterwards from the database.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Op is one kind of request to a probe (be-acceptance probes/CONTRACT.md).
type Op struct {
	Kind    string // echo, work, call, write
	Surface string // call: grpc or http
	Inner   string // call: echo or work
	Bytes   int
	MS      int
	Relay   bool
}

func (o Op) path() string {
	return "/api/probe/" + o.Kind
}

func (o Op) body() []byte {
	var v any
	switch o.Kind {
	case "echo":
		v = map[string]any{"payload": strings.Repeat("e", o.Bytes)}
	case "work":
		v = map[string]any{"ms": o.MS}
	case "call":
		v = map[string]any{"op": o.Inner, "surface": o.Surface, "bytes": o.Bytes, "ms": o.MS}
	case "write":
		v = map[string]any{"bytes": o.Bytes, "relay": o.Relay}
	}
	b, _ := json.Marshal(v)
	return b
}

// Sample is one request: when it was due, how long it took from then, and what came back.
type Sample struct {
	Due     time.Time
	Latency time.Duration // from Due to the answer
	Service time.Duration // from the actual send to the answer
	HopUS   int64         // call: the hop as the caller measured it
	ID      string        // write: the record's identity
	Done    time.Time     // when the answer (or the failure) came
	Status  int           // 0: no answer (transport error or overload)
	Error   string
}

// Plan is one constant-rate run.
type Plan struct {
	Target      string
	Op          Op
	Rate        float64 // requests per second
	Warmup      time.Duration
	Duration    time.Duration
	MaxInFlight int
	Timeout     time.Duration
}

// Run drives the plan and returns the samples of the measured part (after the warmup).
func Run(ctx context.Context, client *http.Client, p Plan) []Sample {
	if p.MaxInFlight == 0 {
		p.MaxInFlight = 4096
	}
	if p.Timeout == 0 {
		p.Timeout = 15 * time.Second
	}
	body, url := p.Op.body(), strings.TrimRight(p.Target, "/")+p.Op.path()
	interval := time.Duration(float64(time.Second) / p.Rate)
	start := time.Now()
	measureFrom, end := start.Add(p.Warmup), start.Add(p.Warmup+p.Duration)
	var (
		mu       sync.Mutex
		samples  []Sample
		wg       sync.WaitGroup
		inFlight atomic.Int64
	)
	for i := 0; ; i++ {
		due := start.Add(time.Duration(i) * interval)
		if !due.Before(end) {
			break
		}
		if d := time.Until(due); d > 0 {
			select {
			case <-ctx.Done():
				wg.Wait()
				return samples
			case <-time.After(d):
			}
		}
		measured := !due.Before(measureFrom)
		if inFlight.Load() >= int64(p.MaxInFlight) {
			if measured { // the driver itself is saturated: counted, never silently skipped
				mu.Lock()
				samples = append(samples, Sample{Due: due, Latency: time.Since(due), Done: time.Now(), Error: "driver overloaded"})
				mu.Unlock()
			}
			continue
		}
		inFlight.Add(1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer inFlight.Add(-1)
			s := send(ctx, client, url, body, due, p.Timeout)
			if measured {
				mu.Lock()
				samples = append(samples, s)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return samples
}

func send(ctx context.Context, client *http.Client, url string, body []byte, due time.Time, timeout time.Duration) Sample {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	sent := time.Now()
	resp, err := client.Do(req)
	s := Sample{Due: due}
	if err != nil {
		s.Latency, s.Service, s.Done, s.Error = time.Since(due), time.Since(sent), time.Now(), err.Error()
		return s
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	s.Latency, s.Service, s.Done, s.Status = time.Since(due), time.Since(sent), time.Now(), resp.StatusCode
	var answer struct {
		ElapsedUS int64  `json:"elapsed_us"`
		ID        string `json:"id"`
		Reason    string `json:"reason"`
	}
	_ = json.Unmarshal(raw, &answer)
	s.HopUS, s.ID = answer.ElapsedUS, answer.ID
	if resp.StatusCode >= 300 {
		s.Error = fmt.Sprintf("%d %s", resp.StatusCode, answer.Reason)
	}
	return s
}

// Percentiles of a set of durations, in milliseconds.
type Percentiles struct {
	P50, P90, P95, P99, Max float64
}

func percentiles(ds []time.Duration) Percentiles {
	if len(ds) == 0 {
		return Percentiles{}
	}
	sorted := append([]time.Duration(nil), ds...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	at := func(q float64) float64 {
		i := int(q*float64(len(sorted))+0.5) - 1
		i = max(0, min(i, len(sorted)-1))
		return float64(sorted[i].Microseconds()) / 1000
	}
	return Percentiles{at(0.50), at(0.90), at(0.95), at(0.99), float64(sorted[len(sorted)-1].Microseconds()) / 1000}
}
