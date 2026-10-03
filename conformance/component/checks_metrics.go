package compconf

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// metricsIssues checks a /metrics exposition (P18.3): every series carries component=<id>,
// the HTTP server metrics exist with their labels, status codes are numeric and no route
// label contains rawToken (a value the suite put into a path).
func metricsIssues(text []byte, id, rawToken string) []string {
	p := expfmt.NewTextParser(model.UTF8Validation)
	fams, err := p.TextToMetricFamilies(bytes.NewReader(text))
	if err != nil {
		return []string{"not the Prometheus text format: " + err.Error()}
	}
	var is []string
	names := make([]string, 0, len(fams))
	for n := range fams {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		for _, m := range fams[n].Metric {
			labels := map[string]string{}
			for _, lp := range m.Label {
				labels[lp.GetName()] = lp.GetValue()
			}
			if labels["component"] != id {
				is = append(is, fmt.Sprintf("%s has component=%q, want %q", n, labels["component"], id))
				break
			}
			if r, ok := labels["route"]; ok && rawToken != "" && strings.Contains(r, rawToken) {
				is = append(is, fmt.Sprintf("%s route %q is a raw path, not a template", n, r))
			}
			if sc, ok := labels["status_code"]; ok {
				if _, err := strconv.Atoi(sc); err != nil {
					is = append(is, fmt.Sprintf("%s status_code %q is not numeric", n, sc))
				}
			}
		}
	}
	need := map[string][]string{
		"be_http_server_requests_total":   {"method", "route", "status_code"},
		"be_http_server_duration_seconds": {"method", "route"},
	}
	for n, labels := range need {
		f, ok := fams[n]
		if !ok || len(f.Metric) == 0 {
			is = append(is, n+" missing")
			continue
		}
		have := map[string]bool{}
		for _, lp := range f.Metric[0].Label {
			have[lp.GetName()] = true
		}
		for _, l := range labels {
			if !have[l] {
				is = append(is, fmt.Sprintf("%s lacks label %s", n, l))
			}
		}
	}
	return is
}
