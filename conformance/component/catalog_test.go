package compconf

import "testing"

func TestLoadCatalogReadsPinnedProtocol(t *testing.T) {
	cat, err := LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if cat.Protocol != "1.0" {
		t.Fatalf("protocol = %q, want 1.0", cat.Protocol)
	}
	if len(cat.Profiles) != 15 {
		t.Fatalf("profiles = %d, want 15", len(cat.Profiles))
	}
	c, ok := cat.Case("CP-CORE-01")
	if !ok || c.Profile != "core" || c.Level != "MUST" {
		t.Fatalf("CP-CORE-01 = %+v", c)
	}
	if len(c.Tests) != 2 || c.Tests[0] != "P1.1" || c.Tests[1] != "P11.1" {
		t.Fatalf("CP-CORE-01 tests = %v", c.Tests)
	}
	counts := map[string]int{}
	for _, c := range cat.Cases {
		counts[c.Profile]++
	}
	want := map[string]int{"core": 14, "obs": 5, "err": 4, "auth": 13}
	for p, n := range want {
		if counts[p] != n {
			t.Errorf("profile %s has %d cases, want %d", p, counts[p], n)
		}
	}
	if got := cat.ProfileNames()[0]; got != "core" {
		t.Errorf("first profile = %s", got)
	}
}

func TestCatalogSkippable(t *testing.T) {
	cat, err := LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	c, _ := cat.Case("CP-SCOPE-11")
	if !c.Skippable {
		t.Fatal("CP-SCOPE-11 must be skippable")
	}
	c, _ = cat.Case("CP-CORE-04")
	if c.Skippable {
		t.Fatal("CP-CORE-04 must not be skippable")
	}
}
