package main

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Metrics is a minimal Prometheus registry: counters, gauges and histograms with labels, in the
// text exposition format. Every series carries component=<ID> (P18.3).
type Metrics struct {
	mu        sync.Mutex
	component string
	families  map[string]*family
	gauges    map[string]func() float64
}

type family struct {
	name, help, typ string
	labels          []string
	series          map[string]*series
}

type series struct {
	values  []string
	value   float64   // counter
	buckets []float64 // histogram: cumulative counts per bound
	sum     float64
	count   float64
}

var defBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

func newMetrics(component string) *Metrics {
	m := &Metrics{component: component, families: map[string]*family{}, gauges: map[string]func() float64{}}
	m.define("be_http_server_requests_total", "HTTP requests served.", "counter", "method", "route", "status_code")
	m.define("be_http_server_duration_seconds", "HTTP request duration.", "histogram", "method", "route")
	m.define("be_grpc_server_handled_total", "gRPC calls handled.", "counter", "service", "method", "code")
	m.define("be_grpc_server_duration_seconds", "gRPC call duration.", "histogram", "service", "method")
	m.define("be_authz_denied_total", "Requests refused by the route guard.", "counter", "reason")
	m.define("be_tx_retries_total", "Transaction bodies re-run after a serialization failure or deadlock.", "counter", "reason")
	m.define("be_db_pool_wait_seconds", "Time waited for a database connection.", "histogram")
	m.define("be_secret_reload_failures_total", "Secret files that could not be re-read.", "counter", "key")
	return m
}

func (m *Metrics) define(name, help, typ string, labels ...string) {
	m.families[name] = &family{name: name, help: help, typ: typ, labels: labels, series: map[string]*series{}}
}

// Gauge registers a gauge read at scrape time.
func (m *Metrics) Gauge(name string, f func() float64) {
	m.mu.Lock()
	m.gauges[name] = f
	m.mu.Unlock()
}

func (m *Metrics) get(name string, values []string) *series {
	f := m.families[name]
	key := strings.Join(values, "\x00")
	s, ok := f.series[key]
	if !ok {
		s = &series{values: values}
		if f.typ == "histogram" {
			s.buckets = make([]float64, len(defBuckets))
		}
		f.series[key] = s
	}
	return s
}

// Inc adds one to a counter.
func (m *Metrics) Inc(name string, values ...string) {
	m.mu.Lock()
	m.get(name, values).value++
	m.mu.Unlock()
}

// Observe records one histogram observation in seconds.
func (m *Metrics) Observe(name string, v float64, values ...string) {
	m.mu.Lock()
	s := m.get(name, values)
	for i, b := range defBuckets {
		if v <= b {
			s.buckets[i]++
		}
	}
	s.sum += v
	s.count++
	m.mu.Unlock()
}

// Write renders the text exposition format 0.0.4.
func (m *Metrics) Write(w io.Writer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.families))
	for n := range m.families {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		m.writeFamily(w, m.families[n])
	}
	gnames := make([]string, 0, len(m.gauges))
	for n := range m.gauges {
		gnames = append(gnames, n)
	}
	sort.Strings(gnames)
	for _, n := range gnames {
		fmt.Fprintf(w, "# TYPE %s gauge\n%s%s %s\n", n, n, m.labelSet(nil, nil), fmtFloat(m.gauges[n]()))
	}
}

func (m *Metrics) writeFamily(w io.Writer, f *family) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", f.name, f.help, f.name, f.typ)
	keys := make([]string, 0, len(f.series))
	for k := range f.series {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := f.series[k]
		if f.typ != "histogram" {
			fmt.Fprintf(w, "%s%s %s\n", f.name, m.labelSet(f.labels, s.values), fmtFloat(s.value))
			continue
		}
		for i, b := range defBuckets {
			fmt.Fprintf(w, "%s_bucket%s %s\n", f.name, m.labelSet(append(f.labels[:len(f.labels):len(f.labels)], "le"),
				append(s.values[:len(s.values):len(s.values)], fmtFloat(b))), fmtFloat(s.buckets[i]))
		}
		fmt.Fprintf(w, "%s_bucket%s %s\n", f.name, m.labelSet(append(f.labels[:len(f.labels):len(f.labels)], "le"),
			append(s.values[:len(s.values):len(s.values)], "+Inf")), fmtFloat(s.count))
		fmt.Fprintf(w, "%s_sum%s %s\n", f.name, m.labelSet(f.labels, s.values), fmtFloat(s.sum))
		fmt.Fprintf(w, "%s_count%s %s\n", f.name, m.labelSet(f.labels, s.values), fmtFloat(s.count))
	}
}

func (m *Metrics) labelSet(names, values []string) string {
	var b strings.Builder
	b.WriteString(`{component="`)
	b.WriteString(escapeLabel(m.component))
	b.WriteByte('"')
	for i, n := range names {
		b.WriteString("," + n + `="` + escapeLabel(values[i]) + `"`)
	}
	b.WriteByte('}')
	return b.String()
}

func escapeLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`).Replace(s)
}

func fmtFloat(f float64) string {
	if math.IsInf(f, 1) {
		return "+Inf"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}
