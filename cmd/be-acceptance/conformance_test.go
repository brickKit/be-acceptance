package main

import (
	"reflect"
	"testing"
)

func TestParseConformanceFlags(t *testing.T) {
	o, err := parseConformanceArgs([]string{"component", "--dir", "c", "--image", "i:1", "--profiles", "core,auth",
		"--dep-contracts", "a/b=x", "--dep-contracts", "c/d=y", "--out", "o"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Dir != "c" || o.Image != "i:1" || o.OutDir != "o" || !reflect.DeepEqual(o.Profiles, []string{"core", "auth"}) {
		t.Fatalf("options = %+v", o)
	}
	if o.DepContracts["a/b"] != "x" || o.DepContracts["c/d"] != "y" {
		t.Fatalf("dep contracts = %v", o.DepContracts)
	}
	for _, bad := range [][]string{{"component", "--image", "i"}, {"component", "--dir", "c"}, {"suite"},
		{"component", "--dir", "c", "--image", "i", "--profiles", "nope"}} {
		if _, err := parseConformanceArgs(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}
