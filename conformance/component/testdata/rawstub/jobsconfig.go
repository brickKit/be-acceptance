package main

import (
	"fmt"
	"sort"
	"time"
)

// jobOverride is one JOBS_OVERRIDES entry (be-protocol schemas/jobs-overrides.schema.json).
type jobOverride struct {
	Interval time.Duration
	Cron     *schedule
	Enabled  *bool
}

// parseJobsOverrides validates JOBS_OVERRIDES (P14.5, P14.6): an invalid schedule or shape is
// a configuration error; an unknown job name is only warned about later.
func parseJobsOverrides(v parsed) (map[string]jobOverride, []configProblem) {
	out := map[string]jobOverride{}
	if !v.set {
		return out, nil
	}
	bad := func(d string) []configProblem { return []configProblem{{Key: "JOBS_OVERRIDES", Class: "CONFIG_INVALID", Detail: d}} }
	m, ok := v.json.(map[string]any)
	if !ok {
		return nil, bad("not a JSON object")
	}
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		e, ok := m[name].(map[string]any)
		if !ok || len(e) == 0 {
			return nil, bad(name + ": not a non-empty object")
		}
		var o jobOverride
		for k, raw := range e {
			switch k {
			case "interval":
				s, _ := raw.(string)
				d, err := time.ParseDuration(s)
				if err != nil || d <= 0 {
					return nil, bad(name + ".interval: not a Go duration")
				}
				o.Interval = d
			case "cron":
				s, _ := raw.(string)
				sc, err := parseSchedule(s)
				if err != nil {
					return nil, bad(fmt.Sprintf("%s.cron: %v", name, err))
				}
				o.Cron = sc
			case "enabled":
				b, ok := raw.(bool)
				if !ok {
					return nil, bad(name + ".enabled: not a boolean")
				}
				o.Enabled = &b
			default:
				return nil, bad(name + ": unknown member " + k)
			}
		}
		out[name] = o
	}
	return out, nil
}

// parseDataLifecycle reads the mode of DATA_LIFECYCLE (P16.9); this runtime has no cold
// adapter, so only the absence of one (none) is accepted.
func parseDataLifecycle(v parsed) (string, []configProblem) {
	if !v.set {
		return "on", nil
	}
	bad := func(d string) []configProblem { return []configProblem{{Key: "DATA_LIFECYCLE", Class: "CONFIG_INVALID", Detail: d}} }
	m, ok := v.json.(map[string]any)
	if !ok {
		return "", bad("not a JSON object")
	}
	mode, _ := m["mode"].(string)
	switch mode {
	case "on", "dry-run", "off":
	default:
		return "", bad("mode must be on, dry-run or off")
	}
	for _, k := range []string{"cold_store", "cold_query"} {
		if t, _ := m[k].(string); t != "" && t != "none" {
			return "", bad(k + ": this runtime has no adapter " + t) // P16.9: fails the start naming it
		}
	}
	return mode, nil
}
