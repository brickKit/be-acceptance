package probe

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/errs"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/list"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The authorization profile's resource (since 3.2.38; the suite's conformance/profile/authz.py): entries with one
// dimension of each kind, an ordinary, a restricted and a secret field, a list, a single object, commands whose
// candidate state is checked, a high-risk approval that can hold its transaction open, and an export with its own
// fields. Only with PROBE_GUARD: an entry is always protected.

// Entry operations, after the variant's prefix and ".entry.".
const (
	EntryRead    = "read"
	EntryCreate  = "create"
	EntryMove    = "move"
	EntryApprove = "approve"
	EntryExport  = "export"
)

// entryResource declares the entry: legal_entity (value set), dept (org tree), owner (principal), amount (amount).
func entryResource(p string) authz.Resource {
	ordinary := authz.Field{Class: authz.Ordinary}
	return authz.Resource{Type: p + ".entry", ViewOperation: p + ".entry." + EntryRead,
		Dimensions: map[string]authz.Dimension{
			"legal_entity": {Kind: authz.ValueSet, Column: "legal_entity"},
			"dept":         {Kind: authz.OrgTree, Column: "dept_path"},
			"owner":        {Kind: authz.Principal, Column: "owner_id", PrincipalRole: "owner"},
			"amount":       {Kind: authz.Amount, Column: "amount", CurrencyColumn: "currency"}},
		Fields: map[string]authz.Field{"id": ordinary, "number": ordinary, "legal_entity": ordinary, "dept_path": ordinary,
			"owner_id": ordinary, "amount": ordinary, "currency": ordinary, "approved": ordinary,
			"cost": {Class: authz.Restricted, ReadKey: p + ".entry.cost"}, "note": {Class: authz.Secret}}}
}

func entryOperations(p string) []authz.Operation {
	t := p + ".entry"
	return []authz.Operation{
		{Key: t + "." + EntryRead, ResourceType: t, Purpose: "read", RiskClass: "low"},
		{Key: t + "." + EntryCreate, ResourceType: t, Purpose: "create", RiskClass: "low", SideEffects: []string{"business_write"}},
		{Key: t + "." + EntryMove, ResourceType: t, Purpose: "action", RiskClass: "low", SideEffects: []string{"business_write"}},
		{Key: t + "." + EntryApprove, ResourceType: t, Purpose: "action", RiskClass: "high", SideEffects: []string{"business_write"}},
		{Key: t + "." + EntryExport, ResourceType: t, Purpose: "export", RiskClass: "low"}}
}

// entry is one row; nil pointers are SQL NULLs (a known null fact).
type entry struct {
	ID          string  `json:"id"`
	Number      string  `json:"number"`
	LegalEntity *string `json:"legal_entity"`
	DeptPath    *string `json:"dept_path"`
	OwnerID     *string `json:"owner_id"`
	Amount      *string `json:"amount"`
	Currency    *string `json:"currency"`
	Cost        string  `json:"cost"`
	Note        string  `json:"note"`
	Approved    bool    `json:"approved"`
}

const entryCols = "id::text, number, legal_entity, dept_path, owner_id::text, amount::text, currency, cost::text, note, approved"

func scanEntry(row pgx.Row) (entry, error) {
	var e entry
	err := row.Scan(&e.ID, &e.Number, &e.LegalEntity, &e.DeptPath, &e.OwnerID, &e.Amount, &e.Currency, &e.Cost, &e.Note, &e.Approved)
	return e, err
}

func fact(v *string) authz.Fact {
	if v == nil {
		return authz.Null
	}
	return authz.Value(*v)
}

// facts are the entry's dimensions as the evaluation takes them.
func (e entry) facts() authz.Facts {
	amount := authz.Null
	if e.Amount != nil && e.Currency != nil {
		amount = authz.Money(*e.Amount, *e.Currency)
	}
	return authz.Facts{"legal_entity": fact(e.LegalEntity), "dept": fact(e.DeptPath), "owner": fact(e.OwnerID), "amount": amount}
}

func (e entry) object() map[string]any {
	return map[string]any{"id": e.ID, "number": e.Number, "legal_entity": e.LegalEntity, "dept_path": e.DeptPath,
		"owner_id": e.OwnerID, "amount": e.Amount, "currency": e.Currency, "cost": e.Cost, "note": e.Note, "approved": e.Approved}
}

func (p *Probe) entryRoutes(add func(method, path, op string, h httpx.Handler)) {
	add("GET", "entries", "entry."+EntryRead, p.listEntries)
	add("POST", "entries", "entry."+EntryCreate, p.createEntry)
	add("GET", "entries/:id", "entry."+EntryRead, p.readEntry)
	add("POST", "entries/:id/move", "entry."+EntryMove, p.moveEntry)
	add("POST", "entries/:id/approve", "entry."+EntryApprove, p.approveEntry)
	add("GET", "entries/:id/export", "entry."+EntryExport, p.exportEntry)
}

// subject is the request's authorization and the entry's declaration (the routes are guarded: both exist).
func (p *Probe) subject(c *httpx.Context) (*authz.Subject, *authz.Resource) {
	s, _ := authz.From(c.Ctx())
	r, _ := s.Defs.Resource(Prefix(p.v.ID) + ".entry")
	return s, r
}

func (p *Probe) load(ctx context.Context, id string) (entry, error) {
	var e entry
	err := p.rt.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		var err error
		e, err = scanEntry(tx.QueryRow(ctx, "SELECT "+entryCols+" FROM probe_entries WHERE id = $1::uuid", id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return e, errs.New(errs.ReasonNotFound, "not found") // the same answer as an invisible one (P8.16)
	}
	return e, err
}

// one decides a stored entry for the route's operation and answers it shaped by the operation's own fields (an export
// outputs the export segments' fields, P8.15) and masked (P8.31).
func (p *Probe) one(c *httpx.Context) error {
	if _, err := uuid.Parse(c.Param("id")); err != nil {
		return errs.New(errs.ReasonNotFound, "not found")
	}
	e, err := p.load(c.Ctx(), c.Param("id"))
	if err != nil {
		return err
	}
	s, r := p.subject(c)
	_, d, err := s.Check(c.Ctx(), c.Operation(), e.facts())
	if err != nil {
		return err
	}
	return c.JSON(200, authz.Mask(r, e.object(), d.Fields))
}

func (p *Probe) readEntry(c *httpx.Context) error   { return p.one(c) }
func (p *Probe) exportEntry(c *httpx.Context) error { return p.one(c) }

// candidate checks a state an entry would take (security 01: a create or a change is judged on the new state as well):
// anything short of allowed is PERMISSION_DENIED (P8.18), unknown stays AUTHORIZATION_UNPROVABLE.
func candidate(ctx context.Context, s *authz.Subject, op string, e entry) (context.Context, error) {
	ctx, d, err := s.Check(ctx, op, e.facts())
	switch {
	case d.Action == authz.True:
		return ctx, nil
	case d.Action == authz.Unknown && d.Visible != authz.False:
		return ctx, err
	case err != nil && !errs.Is(err, "be", errs.ReasonNotFound) && !errs.Is(err, "be", errs.ReasonPermissionDenied):
		return ctx, err // no view, a stale token: as they are
	}
	return ctx, errs.New(errs.ReasonPermissionDenied, "the target state is outside the caller's authorization")
}

type entryIn struct {
	Number      string  `json:"number"`
	LegalEntity *string `json:"legal_entity"`
	DeptPath    *string `json:"dept_path"`
	OwnerID     *string `json:"owner_id"`
	Amount      *string `json:"amount"`
	Currency    *string `json:"currency"`
	Cost        string  `json:"cost"`
	Note        string  `json:"note"`
}

func (p *Probe) createEntry(c *httpx.Context) error {
	var in entryIn
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	if in.Cost == "" {
		in.Cost = "0"
	}
	e := entry{ID: ids.New().String(), Number: in.Number, LegalEntity: in.LegalEntity, DeptPath: in.DeptPath,
		OwnerID: in.OwnerID, Amount: in.Amount, Currency: in.Currency, Cost: in.Cost, Note: in.Note}
	s, _ := p.subject(c)
	ctx, err := candidate(c.Ctx(), s, c.Operation(), e)
	if err != nil {
		return err
	}
	if err := p.rt.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO probe_entries (id, number, legal_entity, dept_path, owner_id, amount, currency, cost, note)
			VALUES ($1, $2, $3, $4, $5::uuid, $6::numeric, $7, $8::numeric, $9)`, e.ID, e.Number, e.LegalEntity, e.DeptPath,
			e.OwnerID, e.Amount, e.Currency, e.Cost, e.Note)
		return err
	}); err != nil {
		return err
	}
	return c.JSON(201, map[string]string{"id": e.ID})
}

// moveEntry changes an entry's dimensions: the stored state must allow the move, and so must the new one.
func (p *Probe) moveEntry(c *httpx.Context) error {
	var in entryIn
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	if _, err := uuid.Parse(c.Param("id")); err != nil {
		return errs.New(errs.ReasonNotFound, "not found")
	}
	e, err := p.load(c.Ctx(), c.Param("id"))
	if err != nil {
		return err
	}
	s, _ := p.subject(c)
	if _, _, err := s.Check(c.Ctx(), c.Operation(), e.facts()); err != nil {
		return err
	}
	for _, f := range []struct{ to, from **string }{{&e.LegalEntity, &in.LegalEntity}, {&e.DeptPath, &in.DeptPath},
		{&e.OwnerID, &in.OwnerID}, {&e.Amount, &in.Amount}, {&e.Currency, &in.Currency}} {
		if *f.from != nil {
			*f.to = *f.from
		}
	}
	ctx, err := candidate(c.Ctx(), s, c.Operation(), e)
	if err != nil {
		return err
	}
	if err := p.rt.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE probe_entries SET legal_entity = $2, dept_path = $3, owner_id = $4::uuid,
			amount = $5::numeric, currency = $6 WHERE id = $1::uuid`, e.ID, e.LegalEntity, e.DeptPath, e.OwnerID, e.Amount, e.Currency)
		return err
	}); err != nil {
		return err
	}
	return c.NoContent(204)
}

type approveIn struct {
	HoldMS int `json:"hold_ms"` // keeps the transaction open after the write (CK-AZ-04: the view runs out meanwhile)
}

// approveEntry is high risk: the write commits only while the view that allowed it is valid (P8.12).
func (p *Probe) approveEntry(c *httpx.Context) error {
	var in approveIn
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	if in.HoldMS < 0 || in.HoldMS > MaxHoldMS {
		return invalid("hold_ms must be 0.." + strconv.Itoa(MaxHoldMS))
	}
	if _, err := uuid.Parse(c.Param("id")); err != nil {
		return errs.New(errs.ReasonNotFound, "not found")
	}
	e, err := p.load(c.Ctx(), c.Param("id"))
	if err != nil {
		return err
	}
	s, _ := p.subject(c)
	ctx, _, err := s.Check(c.Ctx(), c.Operation(), e.facts())
	if err != nil {
		return err
	}
	if err := p.rt.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		if _, err := tx.Exec(ctx, "UPDATE probe_entries SET approved = true WHERE id = $1::uuid", e.ID); err != nil {
			return err
		}
		select {
		case <-time.After(time.Duration(in.HoldMS) * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	}); err != nil {
		return err
	}
	return c.NoContent(204)
}

// The list's columns: every dimension column and the restricted cost filter and sort; number sorts.
var entrySpec = list.Spec{Alias: "e", IDColumn: "id", IDKind: list.UUID, Columns: []list.Column{
	{Field: "number", Column: "number", Kind: list.Text, Sortable: true, Filterable: true},
	{Field: "legal_entity", Column: "legal_entity", Kind: list.Text, Filterable: true},
	{Field: "dept_path", Column: "dept_path", Kind: list.Text, Filterable: true},
	{Field: "owner_id", Column: "owner_id", Kind: list.UUID, Filterable: true},
	{Field: "amount", Column: "amount", Kind: list.Decimal, Filterable: true},
	{Field: "currency", Column: "currency", Kind: list.Text, Filterable: true},
	{Field: "cost", Column: "cost", Kind: list.Decimal, Sortable: true, Filterable: true}}}

// listEntries: ?<column>=<value> filters (eq), sort=<field> or -<field>, readable_only=<field>,..., limit, cursor.
func (p *Probe) listEntries(c *httpx.Context) error {
	s, r := p.subject(c)
	spec := entrySpec
	spec.Operation = c.Operation()
	req := list.Request{Cursor: c.Query("cursor")}
	var err error
	if req.Limit, err = list.ParseLimit(c.Query("limit")); err != nil {
		return err
	}
	for _, col := range spec.Columns {
		if v := c.Query(col.Field); v != "" {
			req.Filters = append(req.Filters, list.Filter{Field: col.Field, Op: "eq", Values: []string{v}})
		}
	}
	if v := c.Query("sort"); v != "" {
		req.Sort = []list.Order{{Field: strings.TrimPrefix(v, "-"), Desc: strings.HasPrefix(v, "-")}}
	}
	if v := c.Query("readable_only"); v != "" {
		req.ReadableOnly = strings.Split(v, ",")
	}
	ctx, q, err := list.Build(c.Ctx(), s, spec, req)
	if err != nil {
		return err
	}
	cols := "e." + strings.ReplaceAll(entryCols, ", ", ", e.")
	if len(q.Hits) > 0 {
		cols += ", " + strings.Join(q.Hits, ", ")
	}
	type row struct {
		e    entry
		hits []bool
	}
	rows, next, err := list.Page(ctx, p.rt.DB, q, "probe_entries", cols, func(rw pgx.Row) (row, error) {
		var x row
		x.hits = make([]bool, len(q.Hits))
		dest := []any{&x.e.ID, &x.e.Number, &x.e.LegalEntity, &x.e.DeptPath, &x.e.OwnerID, &x.e.Amount, &x.e.Currency,
			&x.e.Cost, &x.e.Note, &x.e.Approved}
		for i := range x.hits {
			dest = append(dest, &x.hits[i])
		}
		return x, rw.Scan(dest...)
	}, func(x row) ([]string, string) {
		var keys []string
		for _, f := range q.SortFields() {
			keys = append(keys, map[string]string{"number": x.e.Number, "cost": x.e.Cost}[f])
		}
		return keys, x.e.ID
	})
	if err != nil {
		return err
	}
	items := make([]map[string]any, len(rows))
	for i, x := range rows {
		items[i] = authz.Mask(r, x.e.object(), q.Fields(x.hits))
	}
	out := map[string]any{"items": items, "next": next}
	if len(q.ReadableOnly) > 0 {
		out["readable_only"] = q.ReadableOnly
	}
	return c.JSON(200, out)
}
