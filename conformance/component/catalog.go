// Package compconf is the black-box component conformance suite of be-protocol
// (tools/be-acceptance/conformance/component, informally "compconf"). It runs against a
// running container, never reads source, and judges every language the same way.
package compconf

import (
	"fmt"
	"github.com/brickKit/be-acceptance/conformance/component/protoschema"
	"io/fs"

	beprotocol "github.com/brickKit/be-protocol"
	"gopkg.in/yaml.v3"
)

// Case is one entry of be-protocol schemas/conformance-cases.yaml.
type Case struct {
	ID        string   `yaml:"id"`
	Profile   string   `yaml:"profile"`
	Level     string   `yaml:"level"`
	Skippable bool     `yaml:"skippable"`
	Tests     []string `yaml:"tests"`
	Title     string   `yaml:"title"`
}

// ProfileRule says when a profile applies (prose; selection is in profiles.go).
type ProfileRule struct {
	Name string `yaml:"name"`
	When string `yaml:"when"`
}

// Catalog is the case catalogue of the protocol version the suite pins.
type Catalog struct {
	Protocol string        `yaml:"protocol"`
	Profiles []ProfileRule `yaml:"profiles"`
	Cases    []Case        `yaml:"cases"`
}

// LoadCatalog reads the catalogue from the pinned be-protocol module.
func LoadCatalog() (*Catalog, error) {
	b, err := fs.ReadFile(beprotocol.FS, "schemas/conformance-cases.yaml")
	if err != nil {
		return nil, err
	}
	s, err := protoschema.ProtocolSchema("conformance-cases.schema.json")
	if err != nil {
		return nil, err
	}
	if err := protoschema.ValidateYAML(s, b); err != nil {
		return nil, fmt.Errorf("conformance-cases.yaml: %w", err)
	}
	var c Catalog
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// Case returns the case with this ID.
func (c *Catalog) Case(id string) (Case, bool) {
	for _, k := range c.Cases {
		if k.ID == id {
			return k, true
		}
	}
	return Case{}, false
}

// ProfileNames lists the profiles in catalogue order.
func (c *Catalog) ProfileNames() []string {
	out := make([]string, 0, len(c.Profiles))
	for _, p := range c.Profiles {
		out = append(out, p.Name)
	}
	return out
}

// CasesOf lists the cases of one profile in catalogue order.
func (c *Catalog) CasesOf(profile string) []Case {
	var out []Case
	for _, k := range c.Cases {
		if k.Profile == profile {
			out = append(out, k)
		}
	}
	return out
}
