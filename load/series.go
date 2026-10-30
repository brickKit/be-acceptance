package main

import (
	"regexp"
	"sort"
	"time"
)

// a transport error names the request's URL, which carries document IDs: one key per reason, not per request
var errURL = regexp.MustCompile(`"https?://[^"]*"`)

// Second is one second of a run, by when its requests were due (unix seconds, so it lines up with a fault log): how
// many were sent and answered without error, why the others failed, the latency from when they were due, and in a
// mixed run the confirmations that ended other than “confirmed” (still processing, refused). The deployment
// experiment's suite C (faults) reads it to see in which second a fault starts to show and when the run is normal again.
type Second struct {
	Unix     int64          `json:"unix"`
	Sent     int            `json:"sent"`
	OK       int            `json:"ok"`
	Errors   map[string]int `json:"errors,omitempty"`
	Outcomes map[string]int `json:"outcomes,omitempty"`
	P50      float64        `json:"p50_ms"`
	P95      float64        `json:"p95_ms"`
	Max      float64        `json:"max_ms"`
}

// series buckets the samples by the second they were due, in order; seconds in which nothing was due are left out.
func series(samples []Sample) []Second {
	by := map[int64]*Second{}
	lat := map[int64][]time.Duration{}
	for _, x := range samples {
		u := x.Due.Unix()
		s := by[u]
		if s == nil {
			s = &Second{Unix: u}
			by[u] = s
		}
		s.Sent++
		lat[u] = append(lat[u], x.Latency)
		if x.Outcome != "" {
			if s.Outcomes == nil {
				s.Outcomes = map[string]int{}
			}
			s.Outcomes[x.Outcome]++
		}
		if x.Error != "" {
			if s.Errors == nil {
				s.Errors = map[string]int{}
			}
			s.Errors[errURL.ReplaceAllString(x.Error, `"<url>"`)]++
			continue
		}
		s.OK++
	}
	out := make([]Second, 0, len(by))
	for u, s := range by {
		p := percentiles(lat[u])
		s.P50, s.P95, s.Max = p.P50, p.P95, p.Max
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Unix < out[j].Unix })
	return out
}
