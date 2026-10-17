package probe

import (
	"testing"

	"github.com/brickKit/be-sdk-go/v3/mergesafety"
)

// TestTheProbeIsMergeSafe: the probe module is compiled into Go shells, so it must not touch the process (component
// implementation conventions, "merge safety").
func TestTheProbeIsMergeSafe(t *testing.T) {
	findings, err := mergesafety.CheckDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		t.Errorf("%v", f)
	}
}
