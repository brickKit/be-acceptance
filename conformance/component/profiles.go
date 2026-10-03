package compconf

import "gopkg.in/yaml.v3"

// SelectProfiles applies the rules of be-protocol schemas/conformance-cases.yaml (profiles,
// "when") to the component's manifests and returns the selected profiles in catalogue order.
func SelectProfiles(c *Component) []string {
	hasDB := c.HasConfigKey("PG_SCHEMA") || c.HasConfigKey("PG_HOST")
	rules := []struct {
		name string
		on   bool
	}{
		{"core", true},
		{"obs", true},
		{"err", true},
		{"auth", anyProtected(c)},
		{"scope", scoped(c)},
		{"grpc", c.GRPCPort() != 0},
		{"outbound", len(c.Manifest.Dependencies.Components) > 0},
		{"events-pub", len(c.Manifest.Events.Publishes) > 0},
		{"events-sub", len(c.Manifest.Events.Subscribes) > 0},
		{"idempotency", acceptsIdempotencyKey(c)},
		{"db", hasDB},
		{"jobs", hasDB},
		{"lifecycle", hasDB},
		{"blob", c.HasConfigKey("S3_BUCKET") || c.HasConfigKey("S3_URL")},
		{"shell", len(c.Manifest.Shell.Members) > 0},
	}
	var out []string
	for _, r := range rules {
		if r.on {
			out = append(out, r.name)
		}
	}
	return out
}

func anyProtected(c *Component) bool {
	for _, o := range c.Operations {
		if o.Protected() {
			return true
		}
	}
	return false
}

// scoped: data_scopes is not none, or resources is declared.
func scoped(c *Component) bool {
	if len(c.Assembly.Resources) > 0 {
		return true
	}
	n := c.Assembly.DataScopes
	switch n.Kind {
	case yaml.ScalarNode:
		return n.Value != "" && n.Value != "none"
	case yaml.SequenceNode:
		return len(n.Content) > 0
	}
	return false
}

func acceptsIdempotencyKey(c *Component) bool {
	for _, o := range c.Operations {
		if o.Idempotency {
			return true
		}
	}
	for _, ops := range c.Fixtures.Resources {
		for _, o := range ops {
			if o.Idempotent {
				return true
			}
		}
	}
	return false
}
