package compconf

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SuiteVersion is this suite's version as written into reports.
const SuiteVersion = "0.5.0-dev"

// Report is compconf-report.json (be-protocol schemas/compconf-report.schema.json).
type Report struct {
	Suite      string                   `json:"suite"`
	Protocol   string                   `json:"protocol"`
	Component  string                   `json:"component"`
	Version    string                   `json:"version"`
	Image      reportImage              `json:"image"`
	SDK        json.RawMessage          `json:"sdk,omitempty"`
	Env        map[string]string        `json:"env,omitempty"`
	StartedAt  string                   `json:"started_at"`
	DurationMS int64                    `json:"duration_ms"`
	Result     string                   `json:"result"`
	Profiles   map[string]reportProfile `json:"profiles"`
	Skipped    []SkipItem               `json:"skipped"`
}

type reportImage struct {
	Ref    string `json:"ref"`
	Digest string `json:"digest"`
}

type reportProfile struct {
	Status string       `json:"status"`
	Cases  []reportCase `json:"cases"`
}

type reportCase struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	MS      int64  `json:"ms,omitempty"`
	Message string `json:"message,omitempty"`
}

type reportMeta struct {
	Component, Version    string
	ImageRef, ImageDigest string
	SDK                   json.RawMessage
	Env                   map[string]string
	Started               time.Time
	NotRunReason          string
}

// buildReport turns the evidence into the report. selected = the profiles the manifests
// select; ran = those this run executed. A selected profile that did not run is reported
// skipped. A case is skipped only when it does not apply or assembly.yaml skips it and the
// catalogue marks it skippable; a case the run never reached fails.
func buildReport(cat *Catalog, ev *evidence, selected, ran []string, skips []SkipItem, m reportMeta) Report {
	r := Report{Suite: "be-acceptance@" + SuiteVersion, Protocol: cat.Protocol, Component: m.Component,
		Version: m.Version, Image: reportImage{Ref: m.ImageRef, Digest: m.ImageDigest}, SDK: m.SDK, Env: m.Env,
		StartedAt: m.Started.UTC().Format(time.RFC3339), DurationMS: time.Since(m.Started).Milliseconds(),
		Result: "pass", Profiles: map[string]reportProfile{}, Skipped: []SkipItem{}}
	skipReason := map[string]string{}
	for _, s := range skips {
		if c, ok := cat.Case(s.Case); ok && c.Skippable {
			skipReason[s.Case] = s.Reason
			r.Skipped = append(r.Skipped, s)
		}
	}
	for _, p := range selected {
		prof := reportProfile{Cases: []reportCase{}}
		for _, c := range cat.CasesOf(p) {
			rc := caseResult(c, ev.outcome(c.ID), contains(ran, p), skipReason[c.ID], m.NotRunReason)
			if rc.Status == "fail" && c.Level == "MUST" {
				r.Result = "fail"
			}
			prof.Cases = append(prof.Cases, rc)
		}
		prof.Status = profileStatus(prof.Cases)
		r.Profiles[p] = prof
	}
	return r
}

func caseResult(c Case, o outcome, ran bool, skip, notRun string) reportCase {
	rc := reportCase{ID: c.ID, MS: o.elapsed.Milliseconds()}
	reqs := "[" + strings.Join(c.Tests, " ") + "] "
	switch {
	case !ran:
		rc.Status, rc.Message = "skipped", reqs+"profile not run: "+notRun
	case skip != "":
		rc.Status, rc.Message = "skipped", reqs+"skipped by assembly.yaml: "+skip
	case len(o.fails) > 0:
		rc.Status, rc.Message = "fail", reqs+strings.Join(o.fails, "; ")
		if c.Level == "SHOULD" {
			rc.Status = "warn"
		}
	case o.na != "" && !o.touched:
		rc.Status, rc.Message = "skipped", reqs+"not applicable: "+o.na
	case !o.touched:
		rc.Status, rc.Message = "fail", reqs+"not exercised: the run did not reach this case"
	default:
		rc.Status, rc.Message = "pass", reqs+strings.Join(o.notes, "; ")
		if o.na != "" {
			rc.Message += " (partly not applicable: " + o.na + ")"
		}
	}
	rc.Message = strings.TrimSpace(rc.Message)
	if len(rc.Message) > 2000 {
		rc.Message = rc.Message[:2000] + "…"
	}
	return rc
}

func profileStatus(cases []reportCase) string {
	st := map[string]int{}
	for _, c := range cases {
		st[c.Status]++
	}
	switch {
	case st["fail"] > 0:
		return "fail"
	case st["warn"] > 0:
		return "warn"
	case st["skipped"] == len(cases):
		return "skipped"
	}
	return "pass"
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// Markdown renders the report for people.
func (r Report) Markdown(cat *Catalog) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# compconf %s %s: %s\n\n", r.Component, r.Version, strings.ToUpper(r.Result))
	fmt.Fprintf(&b, "- suite %s, protocol %s, image %s (%s)\n- started %s, %d ms\n",
		r.Suite, r.Protocol, r.Image.Ref, r.Image.Digest, r.StartedAt, r.DurationMS)
	for k, v := range r.Env {
		fmt.Fprintf(&b, "- %s %s\n", k, v)
	}
	for _, p := range cat.ProfileNames() {
		prof, ok := r.Profiles[p]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "\n## %s: %s\n\n| Case | Status | Title | Message |\n|---|---|---|---|\n", p, prof.Status)
		for _, c := range prof.Cases {
			k, _ := cat.Case(c.ID)
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", c.ID, c.Status, k.Title, strings.ReplaceAll(c.Message, "|", "\\|"))
		}
	}
	return b.String()
}
