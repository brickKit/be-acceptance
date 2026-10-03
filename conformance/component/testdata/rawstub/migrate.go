package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type migration struct{ version, name, sql string }

// migrations lists the embedded files <version>_<name>.sql in order.
func migrations() []migration {
	entries, _ := fs.ReadDir(migrationFS, "migrations")
	var out []migration
	for _, e := range entries {
		v, rest, ok := strings.Cut(e.Name(), "_")
		if !ok || !strings.HasSuffix(rest, ".sql") {
			continue
		}
		b, _ := migrationFS.ReadFile("migrations/" + e.Name())
		out = append(out, migration{version: v, name: e.Name(), sql: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out
}

func latestMigration() string {
	ms := migrations()
	if len(ms) == 0 {
		return ""
	}
	return ms[len(ms)-1].version
}

// runMigrate is the migrate entry point (P1.1, P11.1): the owner logs in directly, takes a
// per-schema session advisory lock, and applies each file not yet recorded in
// rawstub_migrations in its own transaction. Run twice, the second run changes nothing.
func runMigrate(cfg *Config, log *Logger) int {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	pw, perr := readSecretFile(cfg.PGOwnerPasswordFile)
	if perr != nil {
		log.Error("config_error", F{"key": "PG_OWNER_PASSWORD_FILE", "error": perr.detail})
		return 78
	}
	cc, err := pgx.ParseConfig(fmt.Sprintf("host=%s port=%d dbname=%s user=%s sslmode=disable connect_timeout=10",
		cfg.PGMigrationHost, cfg.PGMigrationPort, cfg.PGDatabase, cfg.PGOwnerUser))
	if err != nil {
		log.Error("migrate_failed", F{"error": err.Error()})
		return 1
	}
	cc.Password = pw
	cc.RuntimeParams["application_name"] = cfg.ComponentID
	cc.RuntimeParams["TimeZone"] = "UTC"
	conn, err := pgx.ConnectConfig(ctx, cc)
	if err != nil {
		log.Error("migrate_failed", F{"error": err.Error()})
		return 1
	}
	defer conn.Close(context.Background())
	applied, err := migrateWithRetry(ctx, conn, cfg, log)
	if err != nil {
		log.Error("migrate_failed", F{"error": err.Error()})
		return 1
	}
	if err := migrateBus(cfg, log); err != nil { // the third step of the platform migration (P11.3)
		log.Error("migrate_failed", F{"error": err.Error()})
		return 1
	}
	log.Info("migrate_done", F{"applied": applied, "version": latestMigration()})
	return 0
}

// migrateWithRetry retries a lock timeout with backoff up to 3 times; the final failure logs
// the blocking backends (P11.1).
func migrateWithRetry(ctx context.Context, conn *pgx.Conn, cfg *Config, log *Logger) (int, error) {
	for attempt := 0; ; attempt++ {
		n, err := migrateOnce(ctx, conn, cfg, log)
		var pe *pgconn.PgError
		if err == nil || !errors.As(err, &pe) || pe.Code != "55P03" {
			return n, err
		}
		if attempt >= 3 {
			logBlockers(ctx, conn, cfg, log)
			return n, err
		}
		log.Warn("migrate_lock_timeout", F{"attempt": attempt + 1})
		time.Sleep(time.Duration(1<<attempt) * time.Second)
	}
}

func migrateOnce(ctx context.Context, conn *pgx.Conn, cfg *Config, log *Logger) (int, error) {
	session := fmt.Sprintf("SET search_path TO %s; SET lock_timeout = '5s'; SET statement_timeout = '15min'",
		quoteIdent(cfg.PGSchema))
	if _, err := conn.Exec(ctx, session, pgx.QueryExecModeSimpleProtocol); err != nil {
		return 0, err
	}
	lockKey := cfg.PGSchema + ":rawstub_migrations"
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock(hashtext($1))", lockKey); err != nil {
		return 0, err
	}
	defer func() { _, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock(hashtext($1))", lockKey) }()
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS rawstub_migrations (
		version    text        PRIMARY KEY,
		name       text        NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return 0, err
	}
	var current *string
	if err := conn.QueryRow(ctx, "SELECT max(version) FROM rawstub_migrations").Scan(&current); err != nil {
		return 0, err
	}
	if current != nil && *current > latestMigration() {
		log.Warn("migrate_schema_newer", F{"schema_version": *current, "image_version": latestMigration()})
		return 0, nil
	}
	applied := 0
	for _, m := range migrations() {
		var done bool
		if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM rawstub_migrations WHERE version = $1)", m.version).Scan(&done); err != nil {
			return applied, err
		}
		if done {
			continue
		}
		err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, m.sql, pgx.QueryExecModeSimpleProtocol); err != nil {
				return fmt.Errorf("%s: %w", m.name, err)
			}
			_, err := tx.Exec(ctx, "INSERT INTO rawstub_migrations (version, name) VALUES ($1, $2)", m.version, m.name)
			return err
		})
		if err != nil {
			return applied, err
		}
		applied++
		log.Info("migration_applied", F{"version": m.version, "name": m.name})
	}
	if err := applyPlatform(ctx, conn, cfg); err != nil {
		return applied, err
	}
	return applied, nil
}

func logBlockers(ctx context.Context, conn *pgx.Conn, cfg *Config, log *Logger) {
	rows, err := conn.Query(ctx, `SELECT pid, left(query, 200) FROM pg_stat_activity
		WHERE datname = current_database() AND pid <> pg_backend_pid() AND wait_event_type IS DISTINCT FROM 'Client'
		AND state <> 'idle'`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var pid int
		var q string
		if rows.Scan(&pid, &q) == nil {
			log.Error("migrate_blocked_by", F{"pid": pid, "query": q, "schema": cfg.PGSchema})
		}
	}
}

// migrateBus ensures the streams and the durable; it names what it could not create.
func migrateBus(cfg *Config, log *Logger) error {
	bus, err := connectBus(cfg, log)
	if err != nil {
		return err
	}
	defer bus.nc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for {
		err = bus.ensure(ctx)
		if err == nil || ctx.Err() != nil {
			return err
		}
		time.Sleep(time.Second)
	}
}
