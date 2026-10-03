package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// guard is the decision chain of P6.2: Public → allow; verify the token (P5); no bundle yet
// → 503 for every other route, Authenticated included (P1.5, fail closed); the token checks
// of E2 that need the bundle (stale, revoked grant, delegation); Authenticated → allow; the
// route's key → 403 MISSING_PERMISSION.
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
	if b == nil { // before the first bundle every non-Public route fails closed, after the token check
		a.metrics.Inc("be_authz_denied_total", "AUTHZ_NOT_READY")
		return beErr("AUTHZ_NOT_READY", nil)
	}
	if reason, kind := tokenChecks(b, claims); reason != "" {
		a.metrics.Inc("be_authz_denied_total", reason)
		if reason == "TOKEN_STALE" {
			e := beErr(reason, nil)
			e.header = map[string]string{"WWW-Authenticate": `Bearer error="token_stale"`}
			return e
		}
		return beErr(reason, map[string]string{"kind": kind})
	}
	if g == "authenticated" {
		return nil
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
		// The profiles the manifests select (grpc port, PG_SCHEMA): P20.4 requires this list to
		// equal the suite's selection.
		"profiles":   []string{"core", "obs", "err", "auth", "scope", "grpc", "outbound", "idempotency", "db", "jobs", "lifecycle"},
		"ports":      map[string]int{"http": 8080, "grpc": 9090},
		"migrations": map[string]any{"component": comp, "platform": a.platformInfo()},
		"members":    nil,
	})
}

// platformInfo is the platform migration version once readiness saw it applied.
func (a *App) platformInfo() any {
	if a.ready.dbDone() {
		return platformVersion
	}
	return nil
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
