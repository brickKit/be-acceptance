package compconf

import (
	"sort"
	"testing"
)

// Every case of the profiles this suite version implements maps to scenario steps, and no
// step names a case outside them: 1:1 between the catalogue and the tests.
func TestScenarioCoversImplementedProfilesExactly(t *testing.T) {
	cat, err := LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, p := range ImplementedProfiles {
		for _, c := range cat.CasesOf(p) {
			want[c.ID] = true
		}
	}
	got := map[string]bool{}
	for _, s := range scenario() {
		if s.Case == "" { // a setup step that serves several cases
			continue
		}
		if !want[s.Case] {
			t.Errorf("step %q names %s, not a case of an implemented profile", s.Name, s.Case)
		}
		got[s.Case] = true
	}
	var missing []string
	for id := range want {
		if !got[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("cases without a step: %v", missing)
	}
}
