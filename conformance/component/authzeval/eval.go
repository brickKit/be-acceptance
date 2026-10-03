package authzeval

import (
	"regexp"
	"sort"
	"strings"
	"time"
)

var levelRank = map[string]int{"none": 0, "own": 1, "dept": 2, "subtree": 3, "all": 4}

var deptRe = regexp.MustCompile(`^/([^/]+/)*$`)

// Eval evaluates one principal's access at one instant, optionally for one resource type.
type Eval struct {
	b   *Bundle
	c   Claims
	now int64
	t   *ResourceType
}

// New makes an evaluator; t may be nil (key-only questions).
func New(b *Bundle, c Claims, now int64, t *ResourceType) *Eval {
	return &Eval{b: b, c: c, now: now, t: t}
}

func inWindow(from, until *int64, now int64) bool {
	return (from == nil || now >= *from) && (until == nil || now < *until)
}

// activeRoles is E3.
func (e *Eval) activeRoles() []string {
	var out []string
	for _, r := range e.c.Roles {
		if g, ok := e.b.Grants[r]; ok && !inWindow(g.FromTS, g.Until, e.now) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// ceilings is E4: the profiles of ceil[] (unknown = empty, max_level own).
func (e *Eval) ceilings() []Profile {
	var out []Profile
	for _, code := range e.c.Ceil {
		p, ok := e.b.Profiles[code]
		if !ok {
			p = Profile{MaxLevel: "own"}
		}
		out = append(out, p)
	}
	return out
}

func (e *Eval) ceilAllows(k string) bool {
	for _, p := range e.ceilings() {
		if !has(p.Keys, k) && !has(p.Fields, k) {
			return false
		}
	}
	return true
}

func (e *Eval) holders(k string) []string {
	var out []string
	for _, r := range e.activeRoles() {
		if has(e.b.Roles[r], k) {
			out = append(out, r)
		}
	}
	return out
}

// delegations is D_K of E5.
func (e *Eval) delegations(k string) []Delegation {
	if !e.b.Capability("delegation") {
		return nil
	}
	var out []Delegation
	for _, d := range e.b.Delegations {
		if d.Mode == "on_behalf" && d.To == e.c.Sub && has(d.Keys, k) && inWindow(d.FromTS, d.Until, e.now) {
			out = append(out, d)
		}
	}
	return out
}

// Has is E5 has(K).
func (e *Eval) Has(k string) bool {
	return (len(e.holders(k)) > 0 || len(e.delegations(k)) > 0) && e.ceilAllows(k)
}

func (e *Eval) roleLevel(r, k string) string {
	g := e.b.Grants[r]
	if l, ok := g.Levels[k]; ok {
		return l
	}
	if g.DefaultLevel != "" {
		return g.DefaultLevel
	}
	return "own"
}

func (e *Eval) cap() string {
	c := "all"
	for _, p := range e.ceilings() {
		m := p.MaxLevel
		if m == "" {
			m = "own"
		}
		if levelRank[m] < levelRank[c] {
			c = m
		}
	}
	return c
}

// Level is E6 level(K).
func (e *Eval) Level(k string) string {
	hs := e.holders(k)
	if len(hs) == 0 || !e.ceilAllows(k) {
		return "none"
	}
	best := "own"
	for _, r := range hs {
		if l := e.roleLevel(r, k); levelRank[l] > levelRank[best] {
			best = l
		}
	}
	if c := e.cap(); levelRank[c] < levelRank[best] {
		best = c
	}
	return best
}

func (e *Eval) values(k, dim string) []string {
	if !e.ceilAllows(k) {
		return nil
	}
	set := map[string]bool{}
	for _, r := range e.holders(k) {
		for _, v := range e.b.Grants[r].Values[dim] {
			set[v] = true
		}
	}
	return keys(set)
}

func (e *Eval) orgValues(k string) []string {
	c := e.cap()
	var out []string
	for _, v := range e.values(k, "org") {
		if v == "*" && levelRank[c] < levelRank["all"] {
			continue
		}
		if v != "*" && levelRank[c] < levelRank["subtree"] {
			continue
		}
		out = append(out, v)
	}
	return out
}

// Prefix is E6's prefix encoding: a LIKE pattern with backslash escape.
func Prefix(p string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(p) + "%"
}

func (e *Eval) dept() (string, bool) { return e.c.DeptPath, deptRe.MatchString(e.c.DeptPath) }

func (e *Eval) resourceDims() []string {
	if e.t == nil {
		return nil
	}
	var out []string
	for _, d := range e.t.Dimensions {
		if d != "owner" && d != "org" {
			out = append(out, d)
		}
	}
	return out
}

// Params is E6–E9 for key k.
func (e *Eval) Params(k string, graphIDs []string) Params {
	lvl := e.Level(k)
	p := Params{Owners: []string{}, DeptExact: []string{}, DeptPrefix: []string{}, Dims: map[string]Dim{},
		Relations: []string{}, Subjects: []string{}, GraphIDs: []string{}}
	org := e.orgValues(k)
	p.All = lvl != "none" && (lvl == "all" || has(org, "*"))
	owners := map[string]bool{}
	if lvl != "none" {
		owners[e.c.Sub] = true
	}
	if e.ceilAllows(k) {
		for _, d := range e.delegations(k) {
			owners[d.From] = true
		}
	}
	p.Owners = keys(owners)
	dept, valid := e.dept()
	if lvl == "dept" && valid {
		p.DeptExact = []string{dept}
	}
	prefixes := map[string]bool{}
	if lvl == "subtree" && valid {
		prefixes[Prefix(dept)] = true
	}
	for _, v := range org {
		if v != "*" && deptRe.MatchString(v) {
			prefixes[Prefix(v)] = true
		}
	}
	p.DeptPrefix = keys(prefixes)
	for _, d := range e.resourceDims() {
		vals := e.values(k, d)
		dim := Dim{All: has(vals, "*"), IDs: []string{}}
		for _, v := range vals {
			if v != "*" {
				dim.IDs = append(dim.IDs, v)
			}
		}
		p.Dims[d] = dim
	}
	p.Subjects = e.subjects(k)
	p.Relations = e.relations(k)
	p.ACL = len(p.Relations) > 0
	if e.t != nil && e.t.Derivation == "graph" && e.b.Capability("graph") && e.ceilAllows(k) {
		p.GraphIDs = sortedCopy(graphIDs)
	}
	return p
}

// subjects is S(K) of E8.
func (e *Eval) subjects(k string) []string {
	set := map[string]bool{"user:" + e.c.Sub: true}
	for _, r := range e.activeRoles() {
		set["role:"+r] = true
	}
	if d, ok := e.dept(); ok {
		set["dept:"+d] = true
		path := "/"
		set["dept_tree:/"] = true
		for _, s := range strings.Split(strings.Trim(d, "/"), "/") {
			if s == "" {
				continue
			}
			path += s + "/"
			set["dept_tree:"+path] = true
		}
	}
	for _, d := range e.delegations(k) {
		set["user:"+d.From] = true
	}
	return keys(set)
}

func (e *Eval) gives(rel, k string, seen map[string]bool) bool {
	if seen[rel] {
		return false
	}
	seen[rel] = true
	r := e.t.Relations[rel]
	if has(r.Grants, k) {
		return true
	}
	for _, inc := range r.Includes {
		if e.gives(inc, k, seen) {
			return true
		}
	}
	return false
}

func (e *Eval) relations(k string) []string {
	if e.t == nil || !e.ceilAllows(k) {
		return []string{}
	}
	out := []string{}
	for name, r := range e.t.Relations {
		capName := "sharing"
		if r.OwnedBy == "component" {
			capName = "relation_sync"
		}
		if !e.b.Capability(capName) || !e.gives(name, k, map[string]bool{}) {
			continue
		}
		ok := true
		for _, p := range e.ceilings() {
			ok = ok && has(p.Relations, name)
		}
		if ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Degraded is E9.
func (e *Eval) Degraded() []string {
	if e.t != nil && e.t.Derivation == "graph" && !e.b.Capability("graph") {
		return []string{"graph"}
	}
	return []string{}
}

// Fields is E11: masked and read-only columns.
func (e *Eval) Fields() (masked, readOnly []string) {
	masked, readOnly = []string{}, []string{}
	if e.t == nil {
		return
	}
	for _, f := range e.t.Fields {
		switch {
		case !e.Has(f.Read):
			masked = append(masked, f.Columns...)
		case f.Edit == "" || !e.Has(f.Edit):
			readOnly = append(readOnly, f.Columns...)
		}
	}
	sort.Strings(masked)
	sort.Strings(readOnly)
	return
}

// Visible is vis(k, r) of E10.
func (e *Eval) Visible(k string, r Row, acl []ACL, graphIDs []string) bool {
	p := e.Params(k, graphIDs)
	if e.Has(k) && e.identity(p, r) && e.dims(p, r) {
		return true
	}
	if p.ACL {
		for _, a := range acl {
			if a.RType == e.t.Type && a.RID == r.ID && has(p.Relations, a.Relation) && has(p.Subjects, a.Subject) && e.live(a.ExpiresAt) {
				return true
			}
		}
	}
	return has(p.GraphIDs, r.ID)
}

func (e *Eval) live(at *string) bool {
	if at == nil {
		return true
	}
	t, err := time.Parse(time.RFC3339, *at)
	return err == nil && t.Unix() > e.now
}

func (e *Eval) identity(p Params, r Row) bool {
	owner, org := e.t == nil || has(e.t.Dimensions, "owner"), e.t == nil || has(e.t.Dimensions, "org")
	if !owner && !org || p.All {
		return true
	}
	if owner && has(p.Owners, r.Owner) {
		return true
	}
	if org {
		if has(p.DeptExact, r.DeptPath) {
			return true
		}
		for _, pre := range p.DeptPrefix {
			if likePrefix(pre, r.DeptPath) {
				return true
			}
		}
	}
	return false
}

func (e *Eval) dims(p Params, r Row) bool {
	for d, dim := range p.Dims {
		if !dim.All && !has(dim.IDs, r.Values[d]) {
			return false
		}
	}
	return true
}

// likePrefix matches a prefix pattern of Prefix against s.
func likePrefix(pattern, s string) bool {
	raw := strings.NewReplacer(`\\`, `\`, `\%`, `%`, `\_`, `_`).Replace(strings.TrimSuffix(pattern, "%"))
	return strings.HasPrefix(s, raw)
}

// Decide is E10 for route key k.
func (e *Eval) Decide(k string, r Row, acl []ACL, graphIDs []string) Decision {
	view := k
	if e.t != nil && e.t.ViewKey != "" {
		view = e.t.ViewKey
	}
	d := Decision{Visible: e.Visible(view, r, acl, graphIDs)}
	d.Allowed = d.Visible && e.Visible(k, r, acl, graphIDs)
	switch {
	case !d.Visible:
		d.Reason = "NOT_FOUND"
	case d.Allowed:
	case e.Has(k):
		d.Reason = "OUT_OF_SCOPE"
	default:
		d.Reason = "MISSING_PERMISSION"
	}
	return d
}

func has(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedCopy(xs []string) []string {
	out := append([]string{}, xs...)
	sort.Strings(out)
	return out
}
