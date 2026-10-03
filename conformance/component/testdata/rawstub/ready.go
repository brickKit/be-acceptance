package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Readiness latches each condition of P1.4 once met: bundle, db_identity, migrations.
type Readiness struct {
	mu         sync.Mutex
	identityOK bool
	migratedOK bool
	lastIssue  string // the identity problems last logged, so a repeat is not logged again
	bundles    *BundleStore
}

// noteIssue reports whether msg differs from the issue logged last.
func (r *Readiness) noteIssue(msg string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	changed := msg != r.lastIssue
	r.lastIssue = msg
	return changed
}

func (r *Readiness) waiting() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var w []string
	if r.bundles.Current() == nil {
		w = append(w, "bundle")
	}
	if !r.identityOK {
		w = append(w, "db_identity")
	}
	if !r.migratedOK {
		w = append(w, "migrations")
	}
	return w
}

func (r *Readiness) set(identity, migrated bool) {
	r.mu.Lock()
	r.identityOK = r.identityOK || identity
	r.migratedOK = r.migratedOK || migrated
	r.mu.Unlock()
}

func (r *Readiness) dbDone() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.identityOK && r.migratedOK
}

func (r *Readiness) identity() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.identityOK {
		return 1
	}
	return 0
}

// RunDBProbe is the background start-up probe of P10.7 and the migration check of P1.4. A
// connection failure backs off 0.5 s → 15 s; a failed check is retried every second. A
// missing capability, or a schema newer than the image, is fatal.
func RunDBProbe(ctx context.Context, d *DB, r *Readiness, log *Logger, fatal func(error)) {
	supervise(ctx, log, "db_probe", func(ctx context.Context) {
		backoff := 500 * time.Millisecond
		capsDone := false
		for !r.dbDone() {
			var err error
			if !capsDone {
				if err = d.probeCapabilities(ctx); err == nil {
					capsDone = true
				}
			}
			if err == nil {
				err = probeOnce(ctx, d, r, log)
			}
			var fe *fatalError
			if errors.As(err, &fe) {
				fatal(fe)
				return
			}
			wait := time.Second
			if err != nil && isConnErr(err) {
				log.Warn("db_unreachable", F{"error": rootCause(err).Error()})
				wait, backoff = backoff, minDur(backoff*2, 15*time.Second)
			} else {
				backoff = 500 * time.Millisecond
			}
			if !r.dbDone() && !sleepCtx(ctx, wait) {
				return
			}
		}
	})
}

func isConnErr(err error) bool {
	var pe *pgconn.PgError
	return !errors.As(err, &pe)
}

func probeOnce(ctx context.Context, d *DB, r *Readiness, log *Logger) error {
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	problems, err := identityProblems(pctx, d)
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		if msg := strings.Join(problems, "; "); r.noteIssue(msg) {
			log.Error("db_identity_failed", F{"error": msg})
		}
	} else {
		r.set(true, false)
	}
	ver, err := schemaMigrationVersion(pctx, d)
	if err != nil {
		return err
	}
	switch {
	case ver == latestMigration():
		ok, err := platformApplied(pctx, d)
		if err != nil {
			return err
		}
		r.set(false, ok)
	case ver > latestMigration():
		return &fatalError{fmt.Sprintf("schema migration version %s is newer than the image's %s", ver, latestMigration())}
	}
	return nil
}

// identityProblems is the identity part of P10.7, run inside a transaction as PG_USER.
func identityProblems(ctx context.Context, d *DB) ([]string, error) {
	var problems []string
	err := d.Tx(ctx, func(tx pgx.Tx) error {
		problems = nil
		var schema *string
		var usage, create, member bool
		row := tx.QueryRow(ctx, d.Q(`SELECT current_schema(),
			coalesce(has_schema_privilege(current_user, current_schema(), 'USAGE'), false),
			coalesce(has_schema_privilege(current_user, current_schema(), 'CREATE'), false),
			pg_has_role(current_user, $1, 'MEMBER')`), d.cfg.PGOwnerUser)
		if err := row.Scan(&schema, &usage, &create, &member); err != nil {
			return err
		}
		if schema == nil {
			problems = append(problems, "schema not visible to the runtime role")
			return nil
		}
		if !usage {
			problems = append(problems, "runtime role lacks USAGE on the schema")
		}
		if create {
			problems = append(problems, "runtime role holds CREATE on the schema")
		}
		if member {
			problems = append(problems, "runtime role is a member of the owner role")
		}
		rows, err := tx.Query(ctx, d.Q(`SELECT c.relname, pg_get_userbyid(c.relowner),
			has_table_privilege(current_user, c.oid, 'SELECT') AND has_table_privilege(current_user, c.oid, 'INSERT')
			AND has_table_privilege(current_user, c.oid, 'UPDATE') AND has_table_privilege(current_user, c.oid, 'DELETE')
			FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = current_schema() AND c.relkind IN ('r', 'p')`))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name, owner string
			var dml bool
			if err := rows.Scan(&name, &owner, &dml); err != nil {
				return err
			}
			if owner != d.cfg.PGOwnerUser {
				problems = append(problems, "table "+name+" is not owned by the owner role")
			}
			if !dml {
				problems = append(problems, "runtime role lacks DML on table "+name)
			}
		}
		return rows.Err()
	})
	return problems, err
}

// schemaMigrationVersion reads the newest applied version; "" when nothing is applied.
func schemaMigrationVersion(ctx context.Context, d *DB) (string, error) {
	var ver *string
	err := d.Tx(ctx, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, d.Q(`SELECT to_regclass('rawstub_migrations') IS NOT NULL`)).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return nil
		}
		return tx.QueryRow(ctx, d.Q(`SELECT max(version) FROM rawstub_migrations`)).Scan(&ver)
	})
	if err != nil || ver == nil {
		return "", err
	}
	return *ver, nil
}
