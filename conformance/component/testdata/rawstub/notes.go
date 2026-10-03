package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The notes resource: create, list, get, archive (a command), and the slow operation.

type note struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	OwnerID    string  `json:"owner_id"`
	DeptPath   string  `json:"dept_path"`
	Kind       string  `json:"kind"`
	CreatedAt  string  `json:"created_at"`
	ArchivedAt *string `json:"archived_at"`
	Version    int64   `json:"version"`
}

// noteCols is the column list scanNote reads.
const noteCols = "id::text, title, owner_id, dept_path, kind, created_at, archived_at, version"

const tsLayout = "2006-01-02T15:04:05.000000Z"

func scanNote(row pgx.Row) (note, error) {
	var n note
	var at time.Time
	var arch *time.Time
	err := row.Scan(&n.ID, &n.Title, &n.OwnerID, &n.DeptPath, &n.Kind, &at, &arch, &n.Version)
	n.CreatedAt = at.UTC().Format(tsLayout)
	if arch != nil {
		s := arch.UTC().Format(tsLayout)
		n.ArchivedAt = &s
	}
	return n, err
}

func (a *App) listNotes(rc *reqCtx) error {
	if rc.r.URL.Query().Get("cursor") != "" {
		return beErr("CURSOR_INVALID", nil)
	}
	items := []note{}
	err := a.db.Tx(rc.ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(rc.ctx, a.db.Q(`SELECT `+noteCols+` FROM notes ORDER BY created_at DESC, id DESC LIMIT 50`))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			n, err := scanNote(rows)
			if err != nil {
				return err
			}
			items = append(items, n)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	return writeJSON(rc, http.StatusOK, map[string]any{"items": items, "next_cursor": ""})
}

var kindRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

func (a *App) createNote(rc *reqCtx) error {
	b, err := readBody(rc)
	if err != nil {
		return err
	}
	var in struct {
		Title *string `json:"title"`
		Kind  string  `json:"kind"`
	}
	if json.Unmarshal(b, &in) != nil || in.Title == nil || strings.TrimSpace(*in.Title) == "" {
		return ownErr("NOTE_TITLE_REQUIRED", nil)
	}
	if in.Kind == "" {
		in.Kind = "plain"
	}
	if !kindRe.MatchString(in.Kind) {
		return ownErr("NOTE_TITLE_REQUIRED", nil)
	}
	key, err := idemKey(rc, b)
	if err != nil {
		return err
	}
	insert := func(tx pgx.Tx) (int, any, error) {
		now := time.Now().UTC().Truncate(time.Millisecond)
		n := note{ID: newUUIDv7(now), Title: *in.Title, OwnerID: rc.claims.Sub, DeptPath: rc.claims.DeptPath, Kind: in.Kind,
			CreatedAt: now.Format(tsLayout), Version: 1}
		_, err := tx.Exec(rc.ctx, a.db.Q(`INSERT INTO notes (id, title, owner_id, dept_path, kind, created_at, version)
			VALUES ($1, $2, $3, $4, $5, $6, 1)`), n.ID, n.Title, n.OwnerID, n.DeptPath, n.Kind, now)
		return http.StatusCreated, n, err
	}
	if key == "" {
		var st int
		var n any
		err := a.db.Tx(rc.ctx, func(tx pgx.Tx) error { var e error; st, n, e = insert(tx); return e })
		if err != nil {
			return err
		}
		return writeJSON(rc, st, n)
	}
	call := idemCall{caller: "user:" + rc.claims.Sub, key: key, command: rc.route.guard,
		hash: fingerprint(map[string]string{"title": *in.Title, "kind": in.Kind})}
	st, body, err := a.runIdempotent(rc.ctx, call, insert)
	if err != nil {
		return err
	}
	return writeStored(rc, st, body)
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// getNoteByID is shared by REST and gRPC: NOTE_ID_INVALID, NOT_FOUND or the note.
func (a *App) getNoteByID(ctx context.Context, id string) (note, error) {
	if !uuidRe.MatchString(id) {
		return note{}, ownErr("NOTE_ID_INVALID", map[string]string{"id": id})
	}
	var n note
	found := false
	err := a.db.Tx(ctx, func(tx pgx.Tx) error {
		var err error
		n, err = scanNote(tx.QueryRow(ctx, a.db.Q(`SELECT `+noteCols+` FROM notes WHERE id = $1`), strings.ToLower(id)))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		found = err == nil
		return err
	})
	if err != nil {
		return note{}, err
	}
	if !found {
		return note{}, beErr("NOT_FOUND", nil)
	}
	return n, nil
}

func (a *App) getNote(rc *reqCtx) error {
	n, err := a.getNoteByID(rc.ctx, rc.params["id"])
	if err != nil {
		return err
	}
	return writeJSON(rc, http.StatusOK, n)
}

// archiveNote is a one-step idempotent command (fingerprint: no field): it locks the row (a held
// lock answers 409 LOCK_TIMEOUT after lock_timeout, P10.3), sets archived_at once and bumps
// the version. The order is P13.5: arguments, the target, then the key.
func (a *App) archiveNote(rc *reqCtx) error {
	id := strings.ToLower(rc.params["id"])
	if !uuidRe.MatchString(id) {
		return ownErr("NOTE_ID_INVALID", map[string]string{"id": id})
	}
	b, err := readBody(rc)
	if err != nil {
		return err
	}
	key, err := idemKey(rc, b)
	if err != nil {
		return err
	}
	if _, err := a.getNoteByID(rc.ctx, id); err != nil {
		return err
	}
	archive := func(tx pgx.Tx) (int, any, error) {
		n, err := scanNote(tx.QueryRow(rc.ctx, a.db.Q(`SELECT `+noteCols+` FROM notes WHERE id = $1 FOR UPDATE`), id))
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, beErr("NOT_FOUND", nil)
		}
		if err != nil || n.ArchivedAt != nil {
			return http.StatusOK, n, err
		}
		n, err = scanNote(tx.QueryRow(rc.ctx, a.db.Q(`UPDATE notes SET archived_at = now(), version = version + 1
			WHERE id = $1 RETURNING `+noteCols), id))
		return http.StatusOK, n, err
	}
	if key == "" {
		var st int
		var n any
		if err := a.db.Tx(rc.ctx, func(tx pgx.Tx) error { var e error; st, n, e = archive(tx); return e }); err != nil {
			return err
		}
		return writeJSON(rc, st, n)
	}
	call := idemCall{caller: "user:" + rc.claims.Sub, key: key, command: rc.route.guard, target: id, hash: fingerprint(nil)}
	st, body, err := a.runIdempotent(rc.ctx, call, archive)
	if err != nil {
		return err
	}
	return writeStored(rc, st, body)
}

// slow waits ms milliseconds within the route deadline (5 s): by sleeping, by pg_sleep inside
// a transaction (via=db), or in the dependency (via=peer, outbound.go).
func (a *App) slow(rc *reqCtx) error {
	ms, err := strconv.Atoi(rc.r.URL.Query().Get("ms"))
	if err != nil || ms < 0 {
		ms = 0 // lenient: the catalogue has no reason for a malformed ms
	}
	switch rc.r.URL.Query().Get("via") {
	case "db":
		err := a.db.Tx(rc.ctx, func(tx pgx.Tx) error {
			_, err := tx.Exec(rc.ctx, a.db.Q(`SELECT pg_sleep($1::float8 / 1000)`), ms)
			return err
		})
		if err != nil {
			return err
		}
	default:
		select {
		case <-time.After(time.Duration(ms) * time.Millisecond):
		case <-rc.ctx.Done():
			return beErrCause("DEADLINE_BUDGET_EXHAUSTED", rc.ctx.Err())
		}
	}
	return writeJSON(rc, http.StatusOK, map[string]int{"waited_ms": ms})
}
