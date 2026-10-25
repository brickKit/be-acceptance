package main

import (
	"context"
	"net/http"
	"time"
)

// Summary is what one constant-rate run reports.
type Summary struct {
	Target       string                 `json:"target"`
	Op           Op                     `json:"op"`
	Rate         float64                `json:"rate"`
	Seconds      float64                `json:"seconds"`
	Sent         int                    `json:"sent"`
	OK           int                    `json:"ok"`
	Errors       map[string]int         `json:"errors"`
	ErrorRate    float64                `json:"error_rate"`
	Throughput   float64                `json:"throughput"` // answered without error within the measured window, per second
	Latency      Percentiles            `json:"latency_ms"` // from when each request was due
	Service      Percentiles            `json:"service_ms"` // from when each request was actually sent
	Hop          *Percentiles           `json:"hop_ms,omitempty"`
	Writes       []Write                `json:"writes,omitempty"`
	DriverIssues int                    `json:"driver_overloaded"`
	Flow         string                 `json:"flow,omitempty"`  // a business chain: its flow
	Steps        map[string]StepSummary `json:"steps,omitempty"` // and each of its requests
	Mix          *MixSummary            `json:"mix,omitempty"`   // a mixed run: each op, fallbacks, confirm to billed
}

// Write is a write's identity and when it was due, for its completion time taken from the database afterwards.
type Write struct {
	ID    string `json:"id"`
	DueNS int64  `json:"due_unix_ns"`
}

func summarize(p Plan, samples []Sample) Summary {
	s := Summary{Target: p.Target, Op: p.Op, Rate: p.Rate, Seconds: p.Duration.Seconds(), Sent: len(samples),
		Errors: map[string]int{}}
	var lat, svc, hop []time.Duration
	for _, x := range samples {
		lat, svc = append(lat, x.Latency), append(svc, x.Service)
		if x.Error != "" {
			s.Errors[x.Error]++
			if x.Error == "driver overloaded" {
				s.DriverIssues++
			}
			continue
		}
		s.OK++
		if x.HopUS > 0 {
			hop = append(hop, time.Duration(x.HopUS)*time.Microsecond)
		}
		if x.ID != "" {
			s.Writes = append(s.Writes, Write{x.ID, x.Due.UnixNano()})
		}
	}
	if s.Sent > 0 {
		s.ErrorRate = float64(s.Sent-s.OK) / float64(s.Sent)
	}
	// only answers inside the window count: past saturation the rest arrive after it, and counting them would make a
	// saturated target look as if it kept up
	var first time.Time
	for _, x := range samples {
		if first.IsZero() || x.Due.Before(first) {
			first = x.Due
		}
	}
	last := first.Add(p.Duration)
	inWindow := 0
	for _, x := range samples {
		if x.Error == "" && !x.Done.After(last) {
			inWindow++
		}
	}
	s.Throughput = float64(inWindow) / p.Duration.Seconds()
	s.Latency, s.Service = percentiles(lat), percentiles(svc)
	if len(hop) > 0 {
		h := percentiles(hop)
		s.Hop = &h
	}
	if p.Chain != nil {
		s.Flow, s.Steps, s.Writes = p.Chain.Flow, stepSummaries(samples), nil
	}
	if p.Mix != nil {
		s.Flow, s.Steps, s.Writes, s.Mix = "mix", stepSummaries(samples), nil, mixSummary(p.Mix, samples)
	}
	return s
}

// Ramp raises the rate step by step (each step Factor times the last, or plus Step when Factor is 0) until the p99
// latency passes the limit or the error rate passes 1% (experiment plan: "加压到 p99 超过截止时间或错误率超过 1%"), or
// the throughput falls below MinThroughput of the offered rate: past saturation the queue only grows, and within a
// step shorter than the latency limit the p99 alone would never say so.
type Ramp struct {
	Start, Step, Factor, Max float64
	StepTime                 time.Duration
	P99Limit                 time.Duration
	MaxErrorRate             float64
	MinThroughput            float64 // e.g. 0.9; 0: not checked
}

// RampResult is every step and the highest rate that stayed within both limits.
type RampResult struct {
	Steps     []Summary `json:"steps"`
	Sustained float64   `json:"sustained_rate"`
	StoppedBy string    `json:"stopped_by"`
}

func ramp(ctx context.Context, client *http.Client, base Plan, r Ramp) RampResult {
	var out RampResult
	next := func(rate float64) float64 {
		if r.Factor > 1 {
			return rate * r.Factor
		}
		return rate + r.Step
	}
	for rate := r.Start; rate <= r.Max; rate = next(rate) {
		p := base
		p.Rate, p.Duration = rate, r.StepTime
		s := summarize(p, Run(ctx, client, p))
		s.Writes = nil // the ramp is about rates, not completions
		out.Steps = append(out.Steps, s)
		switch {
		case s.Latency.P99 > float64(r.P99Limit.Milliseconds()):
			out.StoppedBy = "p99"
			return out
		case s.ErrorRate > r.MaxErrorRate:
			out.StoppedBy = "errors"
			return out
		case r.MinThroughput > 0 && s.Throughput < r.MinThroughput*rate:
			out.StoppedBy = "saturated"
			return out
		}
		out.Sustained = rate
	}
	out.StoppedBy = "max rate"
	return out
}
