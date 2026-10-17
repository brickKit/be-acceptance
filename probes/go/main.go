// Command probe-go runs the Go probe alone (serve, migrate); the Go shells compile in package probe instead.
package main

import (
	"github.com/brickKit/be-acceptance/probes/go/probe"
	"github.com/brickKit/be-sdk-go/v3/launcher"
)

func main() { launcher.Main(probe.Definition()) }
