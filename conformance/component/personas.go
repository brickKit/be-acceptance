package compconf

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/brickKit/be-acceptance/conformance/component/fakes"
	"gopkg.in/yaml.v3"
)

// Suite personas besides the fixtures' users.
const (
	pAll   = "compconf_all"   // a role holding every key at level all, every dimension value *
	pNone  = "compconf_none"  // no role at all
	pStale = "compconf_stale" // like pAll; its sub is made stale by CP-AUTH-08
	// pStaleDeleg is like pAll; CP-AUTH-12 makes its sub stale and sends it a delegated token.
	pStaleDeleg = "compconf_stale_deleg"
	// pAllTwin holds the same roles as pAll under another sub (two callers, CP-IDEM-06).
	pAllTwin = "compconf_all_twin"
)

type persona struct {
	Sub   string
	Roles []string
	Dept  *string
}

func (r *Run) setupPersonas() {
	r.personas = map[string]persona{}
	for name, u := range r.comp.Fixtures.Users {
		sub := u.Sub
		if sub == "" {
			sub = uuidv7()
		}
		r.personas[name] = persona{Sub: sub, Roles: u.Roles, Dept: u.DeptPath}
	}
	dept := "/1/"
	r.personas[pAll] = persona{Sub: uuidv7(), Roles: []string{pAll}, Dept: &dept}
	r.personas[pNone] = persona{Sub: uuidv7(), Dept: &dept}
	r.personas[pStale] = persona{Sub: uuidv7(), Roles: []string{pAll}, Dept: &dept}
	r.personas[pStaleDeleg] = persona{Sub: uuidv7(), Roles: []string{pAll}, Dept: &dept}
	r.personas[pAllTwin] = persona{Sub: uuidv7(), Roles: []string{pAll}, Dept: &dept}
	for code, g := range r.comp.Fixtures.Grants {
		r.authz.SetRole(code, g.Keys, toRoleGrant(g))
	}
	all := fakes.RoleGrant{DefaultLevel: "all", Values: map[string][]string{}}
	for _, d := range r.dimensions() {
		all.Values[d] = []string{"*"}
	}
	r.authz.SetRole(pAll, r.allKeys(), all)
	r.planScope()
}

func toRoleGrant(g Grant) fakes.RoleGrant {
	rg := fakes.RoleGrant{Levels: g.Levels, DefaultLevel: g.DefaultLevel, Values: g.Values}
	switch u := g.Until.(type) {
	case int:
		v := int64(u)
		rg.Until = &v
	case string: // "+60s": relative to now
		if d, err := time.ParseDuration(strings.TrimPrefix(u, "+")); err == nil {
			v := time.Now().Add(d).Unix()
			rg.Until = &v
		}
	}
	return rg
}

// allKeys: every permission key the component mentions, plus the runtime's own keys of the
// lifecycle and operations contracts (P16.8, P14.4).
func (r *Run) allKeys() []string {
	set := map[string]bool{}
	for _, k := range r.runtimeKeys() {
		set[k] = true
	}
	for _, p := range r.comp.Assembly.Permissions {
		set[p.Key] = true
	}
	for _, o := range r.comp.Operations {
		if o.KeyGuarded() {
			set[o.Guard] = true
		}
	}
	for _, ops := range r.comp.Fixtures.Resources {
		for _, o := range ops {
			if o.Key != "" {
				set[o.Key] = true
			}
		}
	}
	return sortedKeys(set)
}

func (r *Run) dimensions() []string {
	var ds []struct {
		Dimension string `yaml:"dimension"`
	}
	if r.comp.Assembly.DataScopes.Kind == yaml.SequenceNode {
		_ = r.comp.Assembly.DataScopes.Decode(&ds)
	}
	set := map[string]bool{}
	for _, d := range ds {
		set[d.Dimension] = true
	}
	return sortedKeys(set)
}

// token mints a token for a persona and remembers it (no log line may ever carry it).
func (r *Run) token(name string, opts ...fakes.TokenOpt) string {
	p, ok := r.personas[name]
	if !ok {
		panic("compconf: unknown persona " + name)
	}
	t := r.iam.Token(fakes.Persona{Sub: p.Sub, Roles: p.Roles, DeptPath: p.Dept}, opts...)
	r.tokens = append(r.tokens, t)
	return t
}

// personaWith returns a fixture persona whose roles grant the key, else pAll.
func (r *Run) personaWith(key string) string {
	for _, name := range sortedKeys(r.comp.Fixtures.Users) {
		for _, role := range r.comp.Fixtures.Users[name].Roles {
			for _, k := range r.comp.Fixtures.Grants[role].Keys {
				if k == key {
					return name
				}
			}
		}
	}
	return pAll
}

var placeholder = regexp.MustCompile(`\{sub:([a-z][a-z0-9_]*)\}|\{id\}|\{[a-z_]+\}`)

// substitute replaces {sub:<persona>}, {id} (with id, or a fresh UUID) and any other
// {param} (with a fresh UUID) in a fixtures string.
func (r *Run) substitute(s, id string) string {
	return placeholder.ReplaceAllStringFunc(s, func(m string) string {
		if strings.HasPrefix(m, "{sub:") {
			if p, ok := r.personas[m[5:len(m)-1]]; ok {
				return p.Sub
			}
			return m
		}
		if m == "{id}" && id != "" {
			return id
		}
		return uuidv7()
	})
}

// uuidv7 makes an RFC 9562 version 7 UUID.
func uuidv7() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	ms := uint64(time.Now().UnixMilli())
	binary.BigEndian.PutUint16(b[0:2], uint16(ms>>32))
	binary.BigEndian.PutUint32(b[2:6], uint32(ms))
	b[6] = (b[6] & 0x0f) | 0x70
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// runtimeKeys are <domain>.<name>.lifecycle.read|thaw|admin and <domain>.<name>.ops.
func (r *Run) runtimeKeys() []string {
	stem := strings.ReplaceAll(r.comp.ID(), "/", ".")
	return []string{stem + ".lifecycle.read", stem + ".lifecycle.thaw", stem + ".lifecycle.admin", stem + ".ops"}
}
