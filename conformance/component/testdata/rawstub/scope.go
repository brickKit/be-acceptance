package main

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Data scopes of the notes resource (P6.3–P6.7; contract-infra-authz EVALUATION.md E3, E5–E7,
// E10). Dimensions: owner (owner_id), org (dept_path) and kind (kind). The stub accepts no
// delegated token (the bundle's delegation capability is false in its deployments), so
// ceilings and on_behalf delegations never apply here.

const (
	resourceType = "conformance.rawstub.note"
	viewKey      = "conformance.rawstub.view"
)

var scopeRank = map[string]int{"own": 1, "dept": 2, "subtree": 3, "all": 4}

var deptRe = regexp.MustCompile(`^/([^/]+/)*$`)

// access is the canonical predicate's parameters for one key (E6, E7).
type access struct {
	has        bool
	all        bool
	owners     []string
	deptExact  []string
	deptPrefix []string
	kindAll    bool
	kinds      []string
}

// activeRoles are the token's roles inside their grant window (E3).
func activeRoles(b *Bundle, c *Claims, now int64) []string {
	var out []string
	for _, r := range c.Roles {
		if g, ok := b.Grants[r]; ok && !inWindow(g.FromTS, g.Until, now) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// likePrefix is E6's prefix encoding.
func likePrefix(p string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(p) + "%"
}

// accessFor evaluates key k for the caller.
func accessFor(b *Bundle, c *Claims, k string, now int64) access {
	var a access
	level := 0
	kinds := map[string]bool{}
	var orgVals []string
	for _, r := range activeRoles(b, c, now) {
		if !contains(b.Roles[r], k) {
			continue
		}
		a.has = true
		g := b.Grants[r]
		l := g.Levels[k]
		if l == "" {
			l = g.DefaultLevel
		}
		if l == "" {
			l = "own"
		}
		level = max(level, scopeRank[l])
		for _, v := range g.Values["kind"] {
			kinds[v] = true
		}
		orgVals = append(orgVals, g.Values["org"]...)
	}
	if !a.has {
		return a
	}
	a.all = level == scopeRank["all"] || contains(orgVals, "*")
	a.owners = []string{c.Sub}
	dept := c.DeptPath
	if broken == "empty-dept-root" && dept == "" {
		dept = "/" // wrong: an empty dept_path is no department, never the root (P6.4)
	}
	if deptRe.MatchString(dept) {
		switch level {
		case scopeRank["dept"]:
			a.deptExact = []string{dept}
		case scopeRank["subtree"]:
			a.deptPrefix = []string{likePrefix(dept)}
		}
	}
	for _, v := range orgVals {
		if v != "*" && deptRe.MatchString(v) {
			a.deptPrefix = append(a.deptPrefix, likePrefix(v))
		}
	}
	for v := range kinds {
		if v == "*" {
			a.kindAll = true
		} else {
			a.kinds = append(a.kinds, v)
		}
	}
	sort.Strings(a.kinds)
	return a
}

// scopePredicate is the canonical predicate (P6.5) with its parameters numbered from n.
func scopePredicate(n int) string {
	p := func(i int) string { return "$" + itoa(n+i) }
	return "((" + p(0) + "::bool OR owner_id = ANY(" + p(1) + ") OR dept_path = ANY(" + p(2) + ") OR dept_path LIKE ANY(" + p(3) + "))" +
		" AND (" + p(4) + "::bool OR kind = ANY(" + p(5) + ")))"
}

func (a access) args() []any {
	nz := func(x []string) []string {
		if x == nil {
			return []string{}
		}
		return x
	}
	return []any{a.all, nz(a.owners), nz(a.deptExact), nz(a.deptPrefix), a.kindAll, nz(a.kinds)}
}

func itoa(i int) string { return strconv.Itoa(i) }

// accessOf is the caller's access for key k under the current bundle.
func (a *App) accessOf(rc *reqCtx, k string) access {
	return accessFor(a.bundles.Current(), rc.claims, k, time.Now().Unix())
}
