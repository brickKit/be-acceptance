// Command probe-go runs one Go probe variant alone (serve, migrate), the one the platform names in COMPONENT_ID; the
// Go shells compile in probe.Definitions() instead. Choosing among compiled definitions is the entry point's job,
// never module code's.
package main

import (
	"os"

	"github.com/brickKit/be-acceptance/probes/go/v3/probe"
	"github.com/brickKit/be-sdk-go/v3/launcher"
)

func main() {
	v, ok := probe.ByID(os.Getenv("COMPONENT_ID"))
	if !ok {
		v = probe.Variants[0] // the launcher then reports the COMPONENT_ID that does not match
	}
	launcher.Main(probe.Definition(v))
}
