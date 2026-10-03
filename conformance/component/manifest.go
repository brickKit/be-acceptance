package compconf

import (
	"fmt"
	"github.com/brickKit/be-acceptance/conformance/component/protoschema"
	"io/fs"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

// Component is everything the suite reads about the component under test: its manifests,
// contracts and fixtures. It never reads source code.
type Component struct {
	FS         fs.FS
	Manifest   Manifest
	Assembly   Assembly
	Fixtures   Fixtures
	Operations []Operation
	Errors     ErrorCatalog
	fixturesAt string
}

// Manifest is the part of component.yaml the suite reads.
type Manifest struct {
	Metadata struct {
		ID      string `yaml:"id"`
		Version string `yaml:"version"`
	} `yaml:"metadata"`
	Dependencies struct {
		Components []yaml.Node `yaml:"components"`
	} `yaml:"dependencies"`
	ConfigSchema struct {
		Properties map[string]ConfigProp `yaml:"properties"`
		Required   []string              `yaml:"required"`
	} `yaml:"configSchema"`
	Deployment struct {
		Image                  string `yaml:"image"`
		Port                   int    `yaml:"port"`
		Protocol               string `yaml:"protocol"`
		StopGracePeriodSeconds int    `yaml:"stopGracePeriodSeconds"`
		ExtraPorts             []Port `yaml:"extraPorts"`
	} `yaml:"deployment"`
	Migration struct {
		Command []string `yaml:"command"`
	} `yaml:"migration"`
	HealthCheck    Check `yaml:"healthCheck"`
	ReadinessCheck Check `yaml:"readinessCheck"`
	Events         struct {
		Publishes  []string `yaml:"publishes"`
		Subscribes []string `yaml:"subscribes"`
	} `yaml:"events"`
	Shell struct {
		Members []yaml.Node `yaml:"members"`
	} `yaml:"shell"`
}

// ConfigProp is one configSchema property.
type ConfigProp struct {
	Type    string `yaml:"type"`
	Default any    `yaml:"default"`
	Secret  bool   `yaml:"secret"`
	Mount   string `yaml:"mount"`
}

// Port is a deployment.extraPorts entry.
type Port struct {
	Name     string `yaml:"name"`
	Port     int    `yaml:"port"`
	Protocol string `yaml:"protocol"`
}

// Check is healthCheck or readinessCheck.
type Check struct {
	Type               string `yaml:"type"`
	Path               string `yaml:"path"`
	StartPeriodSeconds int    `yaml:"startPeriodSeconds"`
}

// Assembly is the part of assembly.yaml the suite reads.
type Assembly struct {
	Protocol    string `yaml:"protocol"`
	Language    string `yaml:"language"`
	Conformance struct {
		Fixtures string     `yaml:"fixtures"`
		Skip     []SkipItem `yaml:"skip"`
	} `yaml:"conformance"`
	DataScopes  yaml.Node `yaml:"data_scopes"`
	Resources   []any     `yaml:"resources"`
	Permissions []struct {
		Key  string `yaml:"key"`
		Type string `yaml:"type"`
	} `yaml:"permissions"`
}

// SkipItem is one assembly.yaml conformance.skip entry.
type SkipItem struct {
	Case   string `yaml:"case"`
	Reason string `yaml:"reason"`
}

// Dependency is one dependencies.components entry.
type Dependency struct {
	ID       string
	Version  string
	Optional bool
}

// ErrorCatalog is a contracts/errors.yaml (or be-protocol errors-be.yaml).
type ErrorCatalog struct {
	Domain  string `yaml:"domain"`
	Reasons []struct {
		Reason string `yaml:"reason"`
		Code   string `yaml:"code"`
		HTTP   int    `yaml:"http"`
	} `yaml:"reasons"`
}

// Has reports whether the catalogue lists the reason.
func (e ErrorCatalog) Has(reason string) bool {
	for _, r := range e.Reasons {
		if r.Reason == reason {
			return true
		}
	}
	return false
}

// LoadComponent reads and validates the manifests, contracts and fixtures under fsys.
func LoadComponent(fsys fs.FS) (*Component, error) {
	c := &Component{FS: fsys}
	if err := readYAML(fsys, "component.yaml", &c.Manifest); err != nil {
		return nil, err
	}
	if err := validateFile(fsys, "assembly.yaml", "assembly-protocol.schema.json"); err != nil {
		return nil, err
	}
	if err := readYAML(fsys, "assembly.yaml", &c.Assembly); err != nil {
		return nil, err
	}
	c.fixturesAt = c.Assembly.Conformance.Fixtures
	if c.fixturesAt == "" {
		c.fixturesAt = "conformance/fixtures.yaml"
	}
	if err := validateFile(fsys, c.fixturesAt, "fixtures.schema.json"); err != nil {
		return nil, err
	}
	if err := readYAML(fsys, c.fixturesAt, &c.Fixtures); err != nil {
		return nil, err
	}
	if _, err := fs.Stat(fsys, "contracts/errors.yaml"); err == nil {
		if err := validateFile(fsys, "contracts/errors.yaml", "errors-yaml.schema.json"); err != nil {
			return nil, err
		}
		if err := readYAML(fsys, "contracts/errors.yaml", &c.Errors); err != nil {
			return nil, err
		}
	}
	ops, err := loadOperations(fsys)
	if err != nil {
		return nil, err
	}
	c.Operations = ops
	return c, nil
}

func readYAML(fsys fs.FS, name string, v any) error {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func validateFile(fsys fs.FS, name, schema string) error {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return err
	}
	s, err := protoschema.ProtocolSchema(schema)
	if err != nil {
		return err
	}
	if err := protoschema.ValidateYAML(s, b); err != nil {
		return fmt.Errorf("%s does not match %s: %w", name, schema, err)
	}
	return nil
}

// ID is metadata.id.
func (c *Component) ID() string { return c.Manifest.Metadata.ID }

// Version is metadata.version.
func (c *Component) Version() string { return c.Manifest.Metadata.Version }

// ServiceName is brickKit's versioned service name: the ID and the version joined by '-',
// every '/' and '.' as '-' (erp/backend 1.0.0 -> erp-backend-1-0-0).
func (c *Component) ServiceName() string {
	r := strings.NewReplacer("/", "-", ".", "-")
	return r.Replace(c.ID()) + "-" + r.Replace(c.Version())
}

// GRPCPort is the extra port named grpc, or 0.
func (c *Component) GRPCPort() int {
	for _, p := range c.Manifest.Deployment.ExtraPorts {
		if p.Name == "grpc" {
			return p.Port
		}
	}
	return 0
}

// Dependencies parses dependencies.components ("id@version" or {id, optional}).
func (c *Component) Dependencies() []Dependency {
	var out []Dependency
	for _, n := range c.Manifest.Dependencies.Components {
		var d Dependency
		ref := n.Value
		if n.Kind == yaml.MappingNode {
			var m struct {
				ID       string `yaml:"id"`
				Optional bool   `yaml:"optional"`
			}
			_ = n.Decode(&m)
			ref, d.Optional = m.ID, m.Optional
		}
		d.ID, d.Version, _ = strings.Cut(ref, "@")
		out = append(out, d)
	}
	return out
}

// HasConfigKey reports whether configSchema declares the key.
func (c *Component) HasConfigKey(k string) bool {
	_, ok := c.Manifest.ConfigSchema.Properties[k]
	return ok
}

// FixtureFile reads a file named relative to the fixtures file.
func (c *Component) FixtureFile(rel string) ([]byte, error) {
	return fs.ReadFile(c.FS, path.Join(path.Dir(c.fixturesAt), rel))
}

// OperationByID finds an OpenAPI operation by operationId.
func (c *Component) OperationByID(id string) (Operation, bool) {
	for _, o := range c.Operations {
		if o.OperationID == id {
			return o, true
		}
	}
	return Operation{}, false
}
