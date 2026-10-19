package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"
)

func main() {
	var (
		p    Plan
		r    Ramp
		out  string
		ramp bool
	)
	flag.StringVar(&p.Target, "target", "", "the probe's user surface, e.g. http://127.0.0.1:38600")
	flag.StringVar(&p.Op.Kind, "op", "echo", "echo, work, call or write")
	flag.StringVar(&p.Op.Surface, "surface", "grpc", "call: grpc or http")
	flag.StringVar(&p.Op.Inner, "inner", "echo", "call: echo or work")
	flag.IntVar(&p.Op.Bytes, "bytes", 1024, "echo, call echo: payload bytes; write: event payload bytes")
	flag.IntVar(&p.Op.MS, "ms", 10, "work, call work: CPU milliseconds")
	flag.BoolVar(&p.Op.Relay, "relay", false, "write: the peer relays the event as a call")
	flag.Float64Var(&p.Rate, "rate", 50, "requests per second (constant, open loop)")
	flag.DurationVar(&p.Warmup, "warmup", 10*time.Second, "not measured")
	flag.DurationVar(&p.Duration, "duration", 60*time.Second, "measured")
	flag.BoolVar(&ramp, "ramp", false, "raise the rate step by step instead (the warmup runs once at -start)")
	flag.Float64Var(&r.Start, "start", 50, "ramp: first rate")
	flag.Float64Var(&r.Step, "step", 50, "ramp: rate added per step (when -factor is 0)")
	flag.Float64Var(&r.Factor, "factor", 1.5, "ramp: each step's rate is the last one's times this; 0: add -step")
	flag.Float64Var(&r.MinThroughput, "min-throughput", 0.9, "ramp: stop when the throughput falls below this share of the offered rate")
	flag.Float64Var(&r.Max, "max", 5000, "ramp: highest rate tried")
	flag.DurationVar(&r.StepTime, "step-time", 30*time.Second, "ramp: measured time per step")
	flag.DurationVar(&r.P99Limit, "p99-limit", 10*time.Second, "ramp: stop when p99 passes it (the route deadline)")
	flag.Float64Var(&r.MaxErrorRate, "max-errors", 0.01, "ramp: stop when the error rate passes it")
	flag.StringVar(&out, "out", "", "result file (JSON); empty: stdout")
	var creds string
	flag.StringVar(&creds, "credentials", "", "a JSON file with token_url, username, password: the probes need a token")
	flag.Parse()
	if p.Target == "" {
		fmt.Fprintln(os.Stderr, "-target is required")
		os.Exit(64)
	}
	client := &http.Client{Transport: &http.Transport{MaxIdleConns: 4096, MaxIdleConnsPerHost: 4096,
		IdleConnTimeout: 90 * time.Second}}
	ctx := context.Background()
	if creds != "" {
		if err := useCredentials(ctx, client, creds); err != nil {
			fmt.Fprintln(os.Stderr, "no token:", err)
			os.Exit(1)
		}
	}
	var result any
	if ramp {
		w := p
		w.Rate, w.Duration, w.Warmup = r.Start, p.Warmup, 0
		Run(ctx, client, w) // warm the connections and the probe
		result = rampRun(ctx, client, p, r)
	} else {
		result = summarize(p, Run(ctx, client, p))
	}
	b, _ := json.MarshalIndent(result, "", "  ")
	if out == "" {
		fmt.Println(string(b))
		return
	}
	if err := os.WriteFile(out, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func rampRun(ctx context.Context, client *http.Client, p Plan, r Ramp) RampResult {
	p.Warmup = 0
	return ramp(ctx, client, p, r)
}
