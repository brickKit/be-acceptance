package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// guard is the decision chain of P6.2: Public → allow; verify the token (P5); the token
// checks of E2 that need the bundle (stale, revoked grant, delegation); Authenticated →
// allow; no bundle → 503; the route's key → 403 MISSING_PERMISSION.
func (a *App) guard(rc *reqCtx) error {
	g := rc.route.guard
	if g == "public" {
		return nil
	}
	rc.perm = g
	claims, err := a.verifier.verify(rc.ctx, rc.r.Header.Get("Authorization"))
	if err != nil {
		a.metrics.Inc("be_authz_denied_total", "TOKEN_INVALID")
		e := beErr("TOKEN_INVALID", nil)
		e.cause = err
		return e
	}
	rc.claims = claims
	b := a.bundles.Current()
	if b != nil {
		if reason, kind := tokenChecks(b, claims); reason != "" {
			a.metrics.Inc("be_authz_denied_total", reason)
			if reason == "TOKEN_STALE" {
				e := beErr(reason, nil)
				e.header = map[string]string{"WWW-Authenticate": `Bearer error="token_stale"`}
				return e
			}
			return beErr(reason, map[string]string{"kind": kind})
		}
	}
	if g == "authenticated" {
		return nil
	}
	if b == nil {
		a.metrics.Inc("be_authz_denied_total", "AUTHZ_NOT_READY")
		return beErr("AUTHZ_NOT_READY", nil)
	}
	if !hasKey(b, claims, g, time.Now().Unix()) {
		a.metrics.Inc("be_authz_denied_total", "MISSING_PERMISSION")
		return beErr("MISSING_PERMISSION", map[string]string{"permission": g})
	}
	return nil
}

func (a *App) healthz(rc *reqCtx) error {
	if a.broken == "healthz-db" {
		ctx, cancel := context.WithTimeout(rc.ctx, 2*time.Second)
		defer cancel()
		err := a.db.Tx(ctx, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, a.db.Q("SELECT 1"))
			return err
		})
		if err != nil {
			e := beErr("NOT_READY", map[string]string{"waiting": "db_identity"})
			e.cause = err
			return e
		}
	}
	rc.w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	rc.w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(rc.w, "ok")
	return nil
}

func (a *App) readyz(rc *reqCtx) error {
	if a.broken == "readyz-live-db" && len(a.ready.waiting()) == 0 {
		ctx, cancel := context.WithTimeout(rc.ctx, 2*time.Second)
		defer cancel()
		if err := a.db.Tx(ctx, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, a.db.Q("SELECT 1"))
			return err
		}); err != nil {
			e := beErr("NOT_READY", map[string]string{"waiting": "db_identity"})
			e.cause = err
			return e
		}
	}
	if w := a.ready.waiting(); len(w) > 0 {
		return beErr("NOT_READY", map[string]string{"waiting": strings.Join(w, ",")})
	}
	rc.w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	rc.w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(rc.w, "ok")
	return nil
}

func (a *App) metricsPage(rc *reqCtx) error {
	rc.w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	rc.w.WriteHeader(http.StatusOK)
	a.metrics.Write(rc.w)
	return nil
}

func (a *App) info(rc *reqCtx) error {
	var comp any
	if v := latestMigration(); v != "" {
		comp = v
	}
	return writeJSON(rc, http.StatusOK, map[string]any{
		"component_id":      a.cfg.ComponentID,
		"component_version": a.cfg.ComponentVersion,
		"protocol":          "1.0",
		"sdk":               nil,
		"language":          map[string]string{"name": "go", "version": strings.TrimPrefix(runtime.Version(), "go")},
		// The profiles the manifests select (grpc port, PG_SCHEMA): P20.4 compares this list
		// with the suite's selection. The stub only passes core, obs, err and auth.
		"profiles":   []string{"core", "obs", "err", "auth", "grpc", "db", "jobs", "lifecycle"},
		"ports":      map[string]int{"http": 8080, "grpc": 9090},
		"migrations": map[string]any{"component": comp, "platform": nil},
		"members":    nil,
	})
}

func (a *App) listKinds(rc *reqCtx) error {
	return writeJSON(rc, http.StatusOK, map[string]any{"items": []map[string]string{
		{"code": "plain", "title": "Plain note"},
		{"code": "checklist", "title": "Checklist"},
	}})
}

// readBody reads the body within the route's limit (P3.6).
func readBody(rc *reqCtx) ([]byte, error) {
	b, err := io.ReadAll(rc.r.Body)
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return nil, beErr("BODY_TOO_LARGE", map[string]string{"limit": strconv.FormatInt(mbe.Limit, 10)})
	}
	return b, err
}

// putOwner logs the caller's profile; phone and email are redacted by the logger's generic
// key rule, not here (P18.2).
func (a *App) putOwner(rc *reqCtx) error {
	b, err := readBody(rc)
	if err != nil {
		return err
	}
	var in struct {
		DisplayName string `json:"display_name"`
		Phone       string `json:"phone"`
		Email       string `json:"email"`
	}
	_ = json.Unmarshal(b, &in) // lenient: the catalogue has no reason for a malformed profile
	a.log.Info("owner_profile_updated", rc.logFields(F{
		"display_name": in.DisplayName, "phone": in.Phone, "email": in.Email,
	}))
	return writeJSON(rc, http.StatusOK, map[string]string{
		"display_name": in.DisplayName, "phone": in.Phone, "email": in.Email,
	})
}

type note struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	OwnerID   string `json:"owner_id"`
	CreatedAt string `json:"created_at"`
}

func scanNote(row pgx.Row) (note, error) {
	var n note
	var at time.Time
	err := row.Scan(&n.ID, &n.Title, &n.OwnerID, &at)
	n.CreatedAt = at.UTC().Format("2006-01-02T15:04:05.000000Z")
	return n, err
}

func (a *App) listNotes(rc *reqCtx) error {
	if rc.r.URL.Query().Get("cursor") != "" {
		return beErr("CURSOR_INVALID", nil)
	}
	items := []note{}
	err := a.db.Tx(rc.ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(rc.ctx, a.db.Q(`SELECT id::text, title, owner_id, created_at FROM notes
			ORDER BY created_at DESC, id DESC LIMIT 50`))
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

func (a *App) createNote(rc *reqCtx) error {
	b, err := readBody(rc)
	if err != nil {
		return err
	}
	var in struct {
		Title *string `json:"title"`
	}
	if json.Unmarshal(b, &in) != nil || in.Title == nil || strings.TrimSpace(*in.Title) == "" {
		return ownErr("NOTE_TITLE_REQUIRED", nil)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	n := note{ID: newUUIDv7(now), Title: *in.Title, OwnerID: rc.claims.Sub, CreatedAt: now.Format("2006-01-02T15:04:05.000000Z")}
	err = a.db.Tx(rc.ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(rc.ctx, a.db.Q(`INSERT INTO notes (id, title, owner_id, created_at) VALUES ($1, $2, $3, $4)`),
			n.ID, n.Title, n.OwnerID, now)
		return err
	})
	if err != nil {
		return err
	}
	return writeJSON(rc, http.StatusCreated, n)
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
		n, err = scanNote(tx.QueryRow(ctx, a.db.Q(`SELECT id::text, title, owner_id, created_at FROM notes WHERE id = $1`),
			strings.ToLower(id)))
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

// slow waits ms milliseconds within the route deadline (5 s).
func (a *App) slow(rc *reqCtx) error {
	ms, err := strconv.Atoi(rc.r.URL.Query().Get("ms"))
	if err != nil || ms < 0 {
		ms = 0 // lenient: the catalogue has no reason for a malformed ms
	}
	select {
	case <-time.After(time.Duration(ms) * time.Millisecond):
	case <-rc.ctx.Done():
		return beErrCause("DEADLINE_BUDGET_EXHAUSTED", rc.ctx.Err())
	}
	return writeJSON(rc, http.StatusOK, map[string]int{"waited_ms": ms})
}
