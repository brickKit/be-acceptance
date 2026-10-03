package compconf

import (
	"strings"
	"testing"
	"time"

	"github.com/brickKit/be-acceptance/conformance/component/protoschema"
)

func sampleMeta() reportMeta {
	return reportMeta{Component: "conformance/rawstub", Version: "1.0.0", ImageRef: "compconf-rawstub:1.0.0",
		ImageDigest: "sha256:" + strings.Repeat("a", 64), Started: time.Unix(1759400000, 0), Env: map[string]string{"postgres": "16.4"}}
}

func TestReportStatusesAndSchema(t *testing.T) {
	cat, err := LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	ev := newEvidence(cat)
	for _, c := range cat.CasesOf("core") {
		if c.ID != "CP-CORE-08" {
			ev.pass(c.ID)
		}
	}
	ev.fail("CP-CORE-04", "healthz answered 503")
	ev.notApplicable("CP-CORE-08", "no reason")
	for _, c := range cat.CasesOf("obs") {
		ev.pass(c.ID)
	}
	rep := buildReport(cat, ev, []string{"core", "obs", "db"}, []string{"core", "obs"}, nil, sampleMeta())
	if rep.Result != "fail" {
		t.Fatalf("result = %s", rep.Result)
	}
	core := rep.Profiles["core"]
	if core.Status != "fail" {
		t.Fatalf("core = %s", core.Status)
	}
	byID := map[string]reportCase{}
	for _, c := range core.Cases {
		byID[c.ID] = c
	}
	if byID["CP-CORE-04"].Status != "fail" || !strings.Contains(byID["CP-CORE-04"].Message, "P1.3") {
		t.Fatalf("CORE-04 = %+v", byID["CP-CORE-04"])
	}
	if byID["CP-CORE-08"].Status != "skipped" {
		t.Fatalf("n/a case = %+v", byID["CP-CORE-08"])
	}
	if rep.Profiles["obs"].Status != "pass" || rep.Profiles["db"].Status != "skipped" {
		t.Fatalf("obs/db = %s %s", rep.Profiles["obs"].Status, rep.Profiles["db"].Status)
	}
	if _, ok := rep.Profiles["err"]; ok {
		t.Fatal("unselected profile reported")
	}
	s, _ := protoschema.ProtocolSchema("compconf-report.schema.json")
	if err := protoschema.ValidateValue(s, rep); err != nil {
		t.Fatalf("report does not match schema: %v", err)
	}
}

func TestUnexercisedCaseFails(t *testing.T) {
	cat, _ := LoadCatalog()
	ev := newEvidence(cat)
	rep := buildReport(cat, ev, []string{"err"}, []string{"err"}, nil, sampleMeta())
	for _, c := range rep.Profiles["err"].Cases {
		if c.Status != "fail" || !strings.Contains(c.Message, "not exercised") {
			t.Fatalf("case %s = %+v", c.ID, c)
		}
	}
}

func TestAssemblySkipOnlyForSkippableCases(t *testing.T) {
	cat, _ := LoadCatalog()
	ev := newEvidence(cat)
	skips := []SkipItem{{Case: "CP-SCOPE-11", Reason: "no field keys"}, {Case: "CP-SCOPE-01", Reason: "nope"}}
	rep := buildReport(cat, ev, []string{"scope"}, []string{"scope"}, skips, sampleMeta())
	if len(rep.Skipped) != 1 || rep.Skipped[0].Case != "CP-SCOPE-11" {
		t.Fatalf("skipped = %+v", rep.Skipped)
	}
	for _, c := range rep.Profiles["scope"].Cases {
		if c.ID == "CP-SCOPE-01" && c.Status != "fail" {
			t.Fatalf("a MUST case cannot be skipped: %+v", c)
		}
		if c.ID == "CP-SCOPE-11" && c.Status != "skipped" {
			t.Fatalf("skippable case = %+v", c)
		}
	}
}

func TestEvidenceRefusesUnknownCase(t *testing.T) {
	cat, _ := LoadCatalog()
	ev := newEvidence(cat)
	defer func() {
		if recover() == nil {
			t.Fatal("unknown case ID accepted")
		}
	}()
	ev.pass("CP-CORE-99")
}
