// Package authzeval is the suite's reference evaluator of contract-infra-authz authz/2
// (EVALUATION.md E1–E11): from a bundle, verified claims and a record's facts it computes the
// decision a component must reach. The scope profile compares the component's answers with
// it. It is checked against every decision vector of the pinned contract (explain, E12, is
// not produced).
package authzeval

import (
	"encoding/json"
	"regexp"
)

// Bundle is the part of bundle.schema.json the rules read.
type Bundle struct {
	Contract      string                     `json:"contract"`
	Capabilities  map[string]json.RawMessage `json:"capabilities"`
	Roles         map[string][]string        `json:"roles"`
	Grants        map[string]Grant           `json:"grants"`
	Profiles      map[string]Profile         `json:"profiles"`
	Delegations   []Delegation               `json:"delegations"`
	StaleSince    map[string]int64           `json:"stale_since"`
	RevokedGrants map[string]json.RawMessage `json:"revoked_grants"`
}

// Grant is grants.<role>.
type Grant struct {
	Levels       map[string]string   `json:"levels,omitempty"`
	DefaultLevel string              `json:"default_level,omitempty"`
	Values       map[string][]string `json:"values,omitempty"`
	FromTS       *int64              `json:"from_ts,omitempty"`
	Until        *int64              `json:"until,omitempty"`
}

// Profile is a ceiling profile.
type Profile struct {
	Keys      []string `json:"keys"`
	Fields    []string `json:"fields"`
	MaxLevel  string   `json:"max_level"`
	Relations []string `json:"relations"`
}

// Delegation is one bundle delegation.
type Delegation struct {
	ID     string   `json:"id"`
	Mode   string   `json:"mode"`
	From   string   `json:"from"`
	To     string   `json:"to"`
	Keys   []string `json:"keys"`
	FromTS *int64   `json:"from_ts"`
	Until  *int64   `json:"until"`
}

// Actor is the act claim.
type Actor struct {
	Sub  string `json:"sub"`
	Kind string `json:"kind"`
	Act  *Actor `json:"act"`
}

// Claims are the verified token's claims.
type Claims struct {
	Sub      string   `json:"sub"`
	Iat      int64    `json:"iat"`
	Roles    []string `json:"roles"`
	DeptPath string   `json:"dept_path"`
	Act      *Actor   `json:"act"`
	Ceil     []string `json:"ceil"`
	Dg       string   `json:"dg"`
}

// Relation is one relation of a resource type.
type Relation struct {
	Grants   []string `json:"grants"`
	Includes []string `json:"includes"`
	OwnedBy  string   `json:"owned_by"`
}

// FieldSet is one field set of a resource type.
type FieldSet struct {
	Set     string   `json:"set"`
	Columns []string `json:"columns"`
	Read    string   `json:"read"`
	Edit    string   `json:"edit"`
}

// ResourceType is a resource declaration (catalog.schema.json resource_type).
type ResourceType struct {
	Type       string              `json:"type"`
	ViewKey    string              `json:"view_key"`
	Keys       []string            `json:"keys"`
	Dimensions []string            `json:"dimensions"`
	Relations  map[string]Relation `json:"relations"`
	Fields     []FieldSet          `json:"fields"`
	Derivation string              `json:"derivation"`
}

// Row is one record's facts.
type Row struct {
	ID       string            `json:"id"`
	Owner    string            `json:"owner"`
	DeptPath string            `json:"dept_path"`
	Values   map[string]string `json:"values"`
}

// ACL is one projection row (besdk_authz_acl).
type ACL struct {
	RType     string  `json:"rtype"`
	RID       string  `json:"rid"`
	Relation  string  `json:"relation"`
	Subject   string  `json:"subject"`
	ExpiresAt *string `json:"expires_at"`
}

// Decision is E10's answer.
type Decision struct {
	Visible bool   `json:"visible"`
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
}

// Dim is one resource dimension's parameters.
type Dim struct {
	All bool     `json:"all"`
	IDs []string `json:"ids"`
}

// Params are the canonical predicate's parameters (E6–E9).
type Params struct {
	All        bool           `json:"s_all"`
	Owners     []string       `json:"s_owners"`
	DeptExact  []string       `json:"s_dept_exact"`
	DeptPrefix []string       `json:"s_dept_prefix"`
	Dims       map[string]Dim `json:"s_dims"`
	ACL        bool           `json:"s_acl"`
	Relations  []string       `json:"s_relations"`
	Subjects   []string       `json:"s_subjects"`
	GraphIDs   []string       `json:"s_graph_ids"`
}

var contractRe = regexp.MustCompile(`^authz/2\.(0|[1-9][0-9]*)$`)

// ParseBundle is E1: the bundle and whether it is accepted.
func ParseBundle(raw []byte) (*Bundle, bool) {
	var b Bundle
	if json.Unmarshal(raw, &b) != nil || !contractRe.MatchString(b.Contract) {
		return nil, false
	}
	return &b, true
}

// Capability is true for a JSON true, or for an object (list_objects).
func (b *Bundle) Capability(name string) bool {
	raw, ok := b.Capabilities[name]
	if !ok {
		return false
	}
	var v bool
	if json.Unmarshal(raw, &v) == nil {
		return v
	}
	var o map[string]any
	return json.Unmarshal(raw, &o) == nil
}

// TokenCheck is E2.
func TokenCheck(b *Bundle, c Claims) string {
	if s, ok := b.StaleSince[c.Sub]; ok && c.Iat < s-5 {
		return "TOKEN_STALE"
	}
	if c.Dg != "" {
		if _, ok := b.RevokedGrants[c.Dg]; ok {
			return "TOKEN_STALE"
		}
	}
	if (c.Act != nil || len(c.Ceil) > 0 || c.Dg != "") && !b.Capability("delegation") {
		return "UNSUPPORTED_DELEGATION"
	}
	for a := c.Act; a != nil; a = a.Act {
		switch {
		case a.Kind == "agent" && b.Capability("agents"), a.Kind == "user" && b.Capability("impersonation"), a.Kind == "svc":
		default:
			return "UNSUPPORTED_DELEGATION"
		}
	}
	return "OK"
}
