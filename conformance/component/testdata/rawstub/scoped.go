package main

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"
)

// visibleUnder reports whether note id exists and passes the predicate of acc.
func (a *App) visibleUnder(ctx context.Context, id string, acc access) (bool, error) {
	if !acc.has {
		return false, nil
	}
	var ok bool
	err := a.db.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, a.db.Q(`SELECT EXISTS (SELECT 1 FROM notes WHERE id = $7 AND `+scopePredicate(1)+`)`),
			append(acc.args(), id)...).Scan(&ok)
	})
	return ok, err
}

// scopedNote is a single read: 404 NOT_FOUND when absent or not visible (P6.6).
func (a *App) scopedNote(rc *reqCtx, id string) (note, error) {
	n, err := a.getNoteByID(rc.ctx, id)
	if err != nil {
		return note{}, err
	}
	vis, err := a.visibleUnder(rc.ctx, n.ID, a.accessOf(rc, viewKey))
	if err != nil {
		return note{}, err
	}
	if !vis && broken == "invisible-403" {
		return note{}, beErr("OUT_OF_SCOPE", nil) // wrong: reveals that the record exists (P6.6)
	}
	if !vis {
		return note{}, beErr("NOT_FOUND", nil)
	}
	return n, nil
}

// decision is E10 for the caller, key k and one note.
type decision struct {
	Visible bool   `json:"visible"`
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
}

func (a *App) decide(rc *reqCtx, k, id string) (decision, error) {
	if !uuidRe.MatchString(id) {
		return decision{Reason: "NOT_FOUND"}, nil
	}
	id = strings.ToLower(id)
	vis, err := a.visibleUnder(rc.ctx, id, a.accessOf(rc, viewKey))
	if err != nil || !vis {
		return decision{Reason: "NOT_FOUND"}, err
	}
	acc := a.accessOf(rc, k)
	ok, err := a.visibleUnder(rc.ctx, id, acc)
	switch {
	case err != nil:
		return decision{}, err
	case ok:
		return decision{Visible: true, Allowed: true}, nil
	case acc.has:
		return decision{Visible: true, Reason: "OUT_OF_SCOPE"}, nil
	}
	return decision{Visible: true, Reason: "MISSING_PERMISSION"}, nil
}

// decideCommand answers 404 for an invisible note and 403 for one outside the command key's
// scope (P6.6).
func (a *App) decideCommand(rc *reqCtx, id string) error {
	d, err := a.decide(rc, rc.route.guard, id)
	switch {
	case err != nil:
		return err
	case d.Reason == "NOT_FOUND":
		return beErr("NOT_FOUND", nil)
	case d.Reason == "MISSING_PERMISSION":
		return beErr("MISSING_PERMISSION", map[string]string{"permission": rc.route.guard})
	case d.Reason != "":
		return beErr(d.Reason, nil)
	}
	return nil
}

// authzCheck is POST _authz/check (P6.10): at most 500 checks, one decision each, in order.
func (a *App) authzCheck(rc *reqCtx) error {
	b, err := readBody(rc)
	if err != nil {
		return err
	}
	var in struct {
		Checks []struct{ Key, Type, ID string } `json:"checks"`
	}
	if json.Unmarshal(b, &in) != nil {
		return beErr("REQUEST_INVALID", nil)
	}
	if len(in.Checks) > 500 {
		e := beErr("BATCH_TOO_LARGE", map[string]string{"field": "checks", "max": "500", "got": itoa(len(in.Checks))})
		e.Violations = []violation{{Field: "checks", Reason: "BATCH_TOO_LARGE"}}
		return e
	}
	out := []decision{}
	for _, c := range in.Checks {
		d := decision{Reason: "NOT_FOUND"}
		if c.Type == resourceType {
			if d, err = a.decide(rc, c.Key, c.ID); err != nil {
				return err
			}
		}
		out = append(out, d)
	}
	return writeJSON(rc, 200, map[string]any{"results": out})
}

// authzExplain is GET _authz/explain: the decision; the stub lists no facts, so nothing of a
// record the caller cannot see is revealed (E12 R62).
func (a *App) authzExplain(rc *reqCtx) error {
	q := rc.r.URL.Query()
	d := decision{Reason: "NOT_FOUND"}
	if q.Get("type") == resourceType {
		var err error
		if d, err = a.decide(rc, q.Get("key"), q.Get("id")); err != nil {
			return err
		}
	}
	word := "not_visible"
	switch {
	case d.Allowed:
		word = "allowed"
	case d.Visible:
		word = "visible"
	}
	return writeJSON(rc, 200, map[string]any{"decision": word, "reasons": []any{}, "missing": []any{}})
}

// shares answers every _shares endpoint: the stub keeps no ACL projection, so sharing is
// unavailable whatever the provider offers (P6.10).
func (a *App) shares(*reqCtx) error {
	return beErr("CAPABILITY_UNAVAILABLE", map[string]string{"capability": "sharing"})
}
