package compconf

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"github.com/brickKit/be-acceptance/conformance/component/authzeval"
	"github.com/brickKit/be-acceptance/conformance/component/fakes"
	"github.com/jackc/pgx/v5"
)

// The scope profile's world: records with designed facts, suite roles and personas, and the
// reference evaluator (authzeval) that says what each persona must see.

// scopeWorld is what the scope cases share.
type scopeWorld struct {
	res        string // fixtures resource
	list, get  FixtureOp
	cmd        *FixtureOp // a command on one record, when the fixtures have one
	cmdName    string
	rtype      *authzeval.ResourceType
	table      string
	cols       map[string]string // dimension -> column
	modes      map[string]string // dimension -> mode
	viewKey    string
	rows       []authzeval.Row
	random     []string // personas with random grants (CP-SCOPE-10)
	seed       uint64
	withChecks bool // the resource contract is mounted (resources declared)
}

// Suite roles and personas of the scope profile.
var scopeLevels = []string{"own", "dept", "subtree", "all"}

func scopePersona(level string) string { return "cc_lvl_" + level }

// scopeKeys are the keys a suite scope role holds: every key the component mentions.
func (r *Run) scopeKeys() []string { return r.allKeys() }

// planScope builds the scope world from the manifests and registers the suite roles and
// personas; it runs at setup, so the first bundle already carries them.
func (r *Run) planScope() {
	if !contains(r.ran, "scope") {
		return
	}
	w := &scopeWorld{cols: map[string]string{}, modes: map[string]string{}}
	for _, d := range r.comp.DataScopes() {
		w.cols[d.Dimension], w.modes[d.Dimension] = d.Column, d.Mode
		if w.table == "" && len(d.Tables) > 0 {
			w.table = d.Tables[0]
		}
	}
	if res := r.comp.Assembly.Resources; len(res) > 0 {
		w.rtype, w.table, w.viewKey, w.withChecks = toResourceType(res[0]), res[0].Table, res[0].ViewKey, true
		if _, ok := r.comp.Fixtures.Resources[res[0].Type]; ok {
			w.res = res[0].Type
		}
	} else {
		w.rtype = &authzeval.ResourceType{Type: "compconf.scope.rows", Derivation: "direct"}
		for _, d := range r.comp.DataScopes() {
			w.rtype.Dimensions = append(w.rtype.Dimensions, d.Dimension)
		}
	}
	if w.res == "" {
		for _, res := range r.creatable() {
			if _, ok := r.comp.Fixtures.Resources[res]["list"]; ok {
				w.res = res
				break
			}
		}
	}
	ops := r.comp.Fixtures.Resources[w.res]
	w.list, w.get = ops["list"], ops["get"]
	if w.viewKey == "" {
		w.viewKey = w.list.Key
	}
	w.rtype.ViewKey = w.viewKey
	for _, name := range sortedKeys(ops) {
		op := ops[name]
		if name != "get" && name != "list" && name != "create" && strings.Contains(op.Path, "{id}") && op.Key != "" && op.Key != w.viewKey && w.cmd == nil {
			w.cmd, w.cmdName = &op, name
		}
	}
	r.scope = w
	r.scopeRoles()
}

func toResourceType(res Resource) *authzeval.ResourceType {
	t := &authzeval.ResourceType{Type: res.Type, ViewKey: res.ViewKey, Keys: res.Keys, Dimensions: res.Dimensions,
		Relations: map[string]authzeval.Relation{}, Derivation: res.Derivation}
	for n, rel := range res.Relations {
		t.Relations[n] = authzeval.Relation{Grants: rel.Grants, Includes: rel.Includes, OwnedBy: rel.OwnedBy}
	}
	for _, f := range res.Fields {
		t.Fields = append(t.Fields, authzeval.FieldSet{Set: f.Set, Columns: f.Columns, Read: f.Read, Edit: f.Edit})
	}
	if t.Derivation == "" {
		t.Derivation = "direct"
	}
	return t
}

// resourceDims are the dimensions other than owner and org.
func (w *scopeWorld) resourceDims() []string {
	var out []string
	for _, d := range w.rtype.Dimensions {
		if d != "owner" && d != "org" && w.cols[d] != "" {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

func (r *Run) addScopeRole(code string, keys []string, g fakes.RoleGrant) { r.authz.SetRole(code, keys, g) }

func (r *Run) addScopePersona(name, dept string, roles ...string) {
	d := dept
	r.personas[name] = persona{Sub: uuidv7(), Roles: roles, Dept: &d}
}

// scopeRoles registers every suite role and persona of the profile.
func (r *Run) scopeRoles() {
	w := r.scope
	star := map[string][]string{}
	for _, d := range w.resourceDims() {
		star[d] = []string{"*"}
	}
	keys := r.scopeKeys()
	for _, l := range scopeLevels {
		r.addScopeRole("cc_role_"+l, keys, fakes.RoleGrant{DefaultLevel: l, Values: star})
		r.addScopePersona(scopePersona(l), "/1/3/", "cc_role_"+l)
	}
	east := map[string][]string{}
	for _, d := range w.resourceDims() {
		east[d] = []string{"cc-east"}
	}
	r.addScopeRole("cc_role_east", keys, fakes.RoleGrant{DefaultLevel: "all", Values: east})
	r.addScopePersona("cc_east", "/1/3/", "cc_role_east")
	r.addScopeRole("cc_role_novalue", keys, fakes.RoleGrant{DefaultLevel: "all"})
	r.addScopePersona("cc_novalue", "/1/3/", "cc_role_novalue")
	if w.cmd != nil {
		r.addScopeRole("cc_role_mixed", keys, fakes.RoleGrant{Levels: map[string]string{w.viewKey: "all", w.cmd.Key: "own"}, DefaultLevel: "all", Values: star})
		r.addScopePersona("cc_mixed", "/1/3/", "cc_role_mixed")
		var viewOnly []string
		for _, k := range keys {
			if k != w.cmd.Key {
				viewOnly = append(viewOnly, k)
			}
		}
		r.addScopeRole("cc_role_viewonly", viewOnly, fakes.RoleGrant{DefaultLevel: "all", Values: star})
		r.addScopePersona("cc_viewonly", "/1/3/", "cc_role_viewonly")
	}
	if len(w.rtype.Fields) > 0 { // CP-SCOPE-11: every key but the first field set's read and edit
		f := w.rtype.Fields[0]
		var masked []string
		for _, k := range keys {
			if k != f.Read && k != f.Edit {
				masked = append(masked, k)
			}
		}
		r.addScopeRole("cc_role_maskme", masked, fakes.RoleGrant{DefaultLevel: "all", Values: star})
		r.addScopePersona("cc_maskme", "/1/3/", "cc_role_maskme")
	}
	r.addScopePersona("cc_multi", "/1/3/", "cc_role_own", "cc_role_subtree")
	r.addScopePersona("cc_nodept", "", "cc_role_subtree")
	past := time.Now().Add(-time.Minute).Unix()
	r.addScopeRole("cc_role_expired", keys, fakes.RoleGrant{DefaultLevel: "all", Values: star, Until: &past})
	r.addScopePersona("cc_expired", "/1/3/", "cc_role_expired")
	r.randomScopeRoles(keys)
}

// randomScopeRoles makes the random grants of CP-SCOPE-10: eight roles with a random level per
// key and random dimension values, and six personas holding one or two of them (or a fixtures
// role) in a random department. The seed is in the report.
func (r *Run) randomScopeRoles(keys []string) {
	w := r.scope
	w.seed = uint64(time.Now().UnixNano())
	rng := rand.New(rand.NewPCG(w.seed, 1))
	pick := func(xs []string) string { return xs[rng.IntN(len(xs))] }
	values := []string{"cc-east", "cc-west", "*"}
	var roles []string
	for i := 0; i < 8; i++ {
		g := fakes.RoleGrant{Levels: map[string]string{}, DefaultLevel: pick(scopeLevels), Values: map[string][]string{}}
		g.Levels[w.viewKey] = pick(scopeLevels)
		for _, d := range w.resourceDims() {
			if rng.IntN(4) > 0 {
				g.Values[d] = []string{pick(values)}
			}
		}
		code := fmt.Sprintf("cc_role_rand%d", i)
		r.addScopeRole(code, keys, g)
		roles = append(roles, code)
	}
	for code := range r.comp.Fixtures.Grants {
		roles = append(roles, code)
	}
	sort.Strings(roles)
	depts := []string{"/1/", "/1/3/", "/1/3/5/", "/1/7/", "/9/", ""}
	for i := 0; i < 6; i++ {
		name := fmt.Sprintf("cc_rand%d", i)
		rs := []string{pick(roles)}
		if rng.IntN(2) == 0 {
			rs = append(rs, pick(roles))
		}
		r.addScopePersona(name, pick(depts), rs...)
		w.random = append(w.random, name)
	}
}

// stepScopeWorld is a setup step: it creates the records and writes their designed facts.
func stepScopeWorld(ctx context.Context, r *Run) {
	w := r.scope
	if w == nil || !r.mainUp {
		return
	}
	if !r.ev.require("CP-SCOPE-01", w.res != "" && w.list.Path != "" && w.table != "", "the scope profile needs a fixtures resource with create and list, and a data_scopes table") {
		r.scope = nil
		return
	}
	owners := []string{r.personas[scopePersona("own")].Sub, r.personas[scopePersona("dept")].Sub, r.personas["cc_nodept"].Sub, uuidv7()}
	depts := []string{"/1/3/", "/1/3/5/", "/1/7/"}
	dimVals := []string{""}
	if len(w.resourceDims()) > 0 {
		dimVals = []string{"cc-east", "cc-west"}
	}
	conn, err := r.dbConn(ctx)
	if !r.ev.require("CP-SCOPE-01", err == nil, "connecting: %v", err) {
		r.scope = nil
		return
	}
	defer conn.Close(ctx)
	for _, o := range owners {
		for _, d := range depts {
			for _, v := range dimVals {
				id, _, err := r.create(ctx, w.res, pAll)
				if !r.ev.require("CP-SCOPE-01", err == nil, "creating a %s: %v", w.res, err) {
					r.scope = nil
					return
				}
				row := authzeval.Row{ID: id, Owner: o, DeptPath: d, Values: map[string]string{}}
				for _, dim := range w.resourceDims() {
					row.Values[dim] = v
				}
				if err := r.writeFacts(ctx, conn, row); !r.ev.require("CP-SCOPE-01", err == nil, "writing the facts of %s: %v", id, err) {
					r.scope = nil
					return
				}
				w.rows = append(w.rows, row)
			}
		}
	}
}

// writeFacts sets the data-scope columns of one record (the suite owns the database).
func (r *Run) writeFacts(ctx context.Context, conn *pgx.Conn, row authzeval.Row) error {
	w := r.scope
	var sets []string
	var args []any
	add := func(col, v string) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", qi(col), len(args)))
	}
	if c := w.cols["owner"]; c != "" {
		add(c, row.Owner)
	}
	if c := w.cols["org"]; c != "" {
		add(c, row.DeptPath)
	}
	for _, d := range w.resourceDims() {
		add(w.cols[d], row.Values[d])
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, row.ID)
	_, err := conn.Exec(ctx, fmt.Sprintf("UPDATE %s SET %s WHERE id::text = $%d", qi(w.table), strings.Join(sets, ", "), len(args)), args...)
	return err
}

// expect is the set of the world's records persona p must see for key k.
func (r *Run) expect(p, k string) map[string]bool {
	b, ok := authzeval.ParseBundle(r.authz.BundleJSON())
	out := map[string]bool{}
	if !ok {
		return out
	}
	ps := r.personas[p]
	c := authzeval.Claims{Sub: ps.Sub, Roles: ps.Roles, Iat: time.Now().Unix()}
	if ps.Dept != nil {
		c.DeptPath = *ps.Dept
	}
	e := authzeval.New(b, c, time.Now().Unix(), r.scope.rtype)
	for _, row := range r.scope.rows {
		if e.Visible(k, row, nil, nil) {
			out[row.ID] = true
		}
	}
	return out
}

// listed pages through the list as persona p and returns which of the world's records it
// returned, with the last non-200 answer.
func (r *Run) listed(ctx context.Context, p string, query map[string]string) (map[string]bool, *exchange) {
	mine := map[string]bool{}
	for _, row := range r.scope.rows {
		mine[row.ID] = true
	}
	got := map[string]bool{}
	cursor := ""
	for page := 0; page < 50; page++ {
		q := map[string]string{}
		for k, v := range query {
			q[k] = v
		}
		x := r.callAt(ctx, r.base, "GET", r.listTarget(q, cursor), withToken(r.token(p)))
		if x.Status != 200 {
			return got, x
		}
		for _, it := range listItems(x.Body) {
			if id, _ := it["id"].(string); mine[id] {
				got[id] = true
			}
		}
		cursor = jsonPathString(x.Body, "$.next_cursor")
		if cursor == "" {
			return got, nil
		}
	}
	return got, nil
}

func (r *Run) listTarget(q map[string]string, cursor string) string {
	l := r.scope.list
	t := r.substitute(l.Path, "")
	parts := []string{"page_size=500"}
	if cursor != "" {
		parts = append(parts, "cursor="+cursor)
	}
	for _, k := range sortedKeys(q) {
		parts = append(parts, k+"="+q[k])
	}
	return t + "?" + strings.Join(parts, "&")
}

// diffSets describes how got differs from want.
func diffSets(want, got map[string]bool) string {
	var missing, extra int
	for id := range want {
		if !got[id] {
			missing++
		}
	}
	for id := range got {
		if !want[id] {
			extra++
		}
	}
	if missing == 0 && extra == 0 {
		return ""
	}
	return fmt.Sprintf("%d of %d expected rows missing, %d unexpected rows", missing, len(want), extra)
}

// rowOf makes a row from (owner, dept_path, region) for tests.
func rowOf(f [3]string) authzeval.Row {
	return authzeval.Row{ID: uuidv7(), Owner: f[0], DeptPath: f[1], Values: map[string]string{"region": f[2]}}
}
