package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DB is the runtime store: it logs in as PG_USER and runs every access inside a transaction
// that begins with the SET LOCAL statements of P10.2 and P10.3.
type DB struct {
	cfg     *Config
	pool    *pgxpool.Pool
	log     *Logger
	metrics *Metrics
	secret  *secretFile
	prefix  string // "/* be:<PG_SCHEMA> */ "
	pgMajor int
	mu      sync.Mutex
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func quoteLit(s string) string   { return `'` + strings.ReplaceAll(s, `'`, `''`) + `'` }

// openDB builds the pool without connecting; connections are made lazily, and each new one
// reads the current password (P2.9).
func openDB(cfg *Config, log *Logger, m *Metrics) (*DB, error) {
	sf, err := newSecretFile("PG_PASSWORD_FILE", cfg.PGPasswordFile, log, m)
	if err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("host=%s port=%d dbname=%s user=%s sslmode=disable", cfg.PGHost, cfg.PGPort, cfg.PGDatabase, cfg.PGUser)
	pc, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	pc.MaxConns = int32(cfg.PGPoolMax)
	if broken == "unbounded-pool" {
		pc.MaxConns = 1000
	}
	pc.MinConns = int32(min(cfg.PGPoolMinIdle, cfg.PGPoolMax))
	pc.MaxConnLifetime = cfg.PGConnMaxLifetime
	pc.MaxConnIdleTime = cfg.PGConnMaxIdleTime
	pc.ConnConfig.ConnectTimeout = 3 * time.Second
	pc.ConnConfig.RuntimeParams["TimeZone"] = "UTC"
	pc.ConnConfig.RuntimeParams["application_name"] = cfg.ComponentID + "@" + cfg.ComponentVersion // P10.2
	pc.MinConns = max(pc.MinConns, 1)                                                               // P10.5: never close the last one
	pc.BeforeConnect = func(_ context.Context, cc *pgx.ConnConfig) error {
		cc.Password = sf.Value()
		return nil
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), pc)
	if err != nil {
		return nil, err
	}
	d := &DB{cfg: cfg, pool: pool, log: log, metrics: m, secret: sf, prefix: "/* be:" + cfg.PGSchema + " */ "}
	m.Gauge("be_db_pool_in_use", func() float64 { return float64(pool.Stat().AcquiredConns()) })
	return d, nil
}

// Q prefixes a statement with the member's schema comment (P10.2).
func (d *DB) Q(sql string) string { return d.prefix + sql }

// dbError marks an error as coming from the database, for the deadline mapping of P3.4.
type dbError struct{ err error }

func (e *dbError) Error() string { return e.err.Error() }
func (e *dbError) Unwrap() error { return e.err }

// Tx runs body in a READ COMMITTED transaction as PG_USER; 40001 and 40P01 re-run the body at
// most 3 attempts in all (P10.4).
func (d *DB) Tx(ctx context.Context, body func(pgx.Tx) error) error {
	for attempt := 1; ; attempt++ {
		err := d.txOnce(ctx, body)
		var pe *pgconn.PgError
		if errors.As(err, &pe) && (pe.Code == "40001" || pe.Code == "40P01") {
			if attempt >= 3 {
				return beErrCause("TX_CONFLICT", err)
			}
			d.metrics.Inc("be_tx_retries_total", map[string]string{"40001": "serialization_failure", "40P01": "deadlock"}[pe.Code])
			delay := time.Duration(10<<(attempt-1))*time.Millisecond + time.Duration(rand.IntN(5))*time.Millisecond
			if !sleepCtx(ctx, delay) {
				return classifyDB(ctx, err)
			}
			continue
		}
		if err != nil {
			return classifyDB(ctx, err)
		}
		return nil
	}
}

func beErrCause(reason string, cause error) *apiError {
	e := beErr(reason, nil)
	e.cause = cause
	return e
}

func (d *DB) txOnce(ctx context.Context, body func(pgx.Tx) error) error {
	acqCtx, cancel := context.WithTimeout(ctx, d.cfg.PGAcquireTimeout)
	start := time.Now()
	conn, err := d.pool.Acquire(acqCtx)
	cancel()
	d.metrics.Observe("be_db_pool_wait_seconds", time.Since(start).Seconds())
	if err != nil {
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return beErrCause("DB_POOL_EXHAUSTED", err)
		}
		return &dbError{err}
	}
	defer conn.Release()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return &dbError{err}
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, d.setLocals(ctx), pgx.QueryExecModeSimpleProtocol); err != nil {
		return &dbError{err}
	}
	if err := body(tx); err != nil {
		return &dbError{err}
	}
	if err := tx.Commit(ctx); err != nil {
		return &dbError{err}
	}
	return nil
}

// setLocals is the opening of every transaction (P10.2, P10.3).
func (d *DB) setLocals(ctx context.Context) string {
	stmt := 5 * time.Second
	remaining := time.Duration(0)
	if dl, ok := ctx.Deadline(); ok {
		remaining = time.Until(dl)
		if remaining < stmt {
			stmt = remaining
		}
	}
	stmt = max(stmt, time.Millisecond)
	var b strings.Builder
	b.WriteString(d.prefix)
	fmt.Fprintf(&b, "SET LOCAL ROLE %s; ", quoteIdent(d.cfg.PGUser))
	fmt.Fprintf(&b, "SET LOCAL search_path TO %s; ", quoteIdent(d.cfg.PGSchema))
	fmt.Fprintf(&b, "SET LOCAL application_name = %s; ", quoteLit(d.cfg.ComponentID))
	fmt.Fprintf(&b, "SET LOCAL statement_timeout = '%dms'; ", stmt.Milliseconds())
	b.WriteString("SET LOCAL lock_timeout = '2s'; ")
	b.WriteString("SET LOCAL idle_in_transaction_session_timeout = '30s'")
	if d.serverMajor() >= 17 && remaining > 0 {
		fmt.Fprintf(&b, "; SET LOCAL transaction_timeout = '%dms'", max(remaining.Milliseconds(), 1))
	}
	return b.String()
}

func (d *DB) serverMajor() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pgMajor
}

// classifyDB maps a database error to the SQLSTATE table of P10.4; everything else is
// INTERNAL with the original kept for the log.
func classifyDB(ctx context.Context, err error) error {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae
	}
	cancelled := errors.Is(ctx.Err(), context.Canceled)
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "55P03":
			return beErrCause("LOCK_TIMEOUT", err)
		case "57014", "25P04":
			if cancelled {
				return beErrCause("REQUEST_CANCELLED", err)
			}
			return beErrCause("STATEMENT_TIMEOUT", err)
		case "53300":
			return beErrCause("DB_TOO_MANY_CONNECTIONS", err)
		case "57P01", "57P02", "57P03", "28P01", "08006", "08001": // the connection was closed or could not be made
			e := beErrCause("DEPENDENCY_UNAVAILABLE", err)
			e.Metadata = map[string]string{"dependency": "db"}
			return e
		}
		return internalErr(err)
	}
	if cancelled {
		return beErrCause("REQUEST_CANCELLED", err)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return beErrCause("STATEMENT_TIMEOUT", err)
	}
	if unreachable(err) { // the database cannot be reached (one reason, metadata.dependency)
		e := beErrCause("DEPENDENCY_UNAVAILABLE", err)
		e.Metadata = map[string]string{"dependency": "db"}
		return e
	}
	return internalErr(err)
}

// secretFile holds a text secret read from its file and re-read when the file's modification
// time or size changes, checked every 30 s (P2.9).
type secretFile struct {
	key, path string
	log       *Logger
	metrics   *Metrics
	mu        sync.Mutex
	value     string
	mtime     time.Time
	size      int64
}

func newSecretFile(key, path string, log *Logger, m *Metrics) (*secretFile, error) {
	s := &secretFile{key: key, path: path, log: log, metrics: m}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	v, perr := readSecretFile(path)
	if perr != nil {
		return nil, errors.New(perr.detail)
	}
	s.value, s.mtime, s.size = v, fi.ModTime(), fi.Size()
	return s, nil
}

func (s *secretFile) Value() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value
}

// Watch re-reads the file when it changes; a failure keeps the last good value.
func (s *secretFile) Watch(ctx context.Context) {
	if broken == "secret-read-once" {
		return
	}
	for sleepCtx(ctx, 10*time.Second) { // at most 30 s apart (P2.9)
		fi, err := os.Stat(s.path)
		s.mu.Lock()
		changed := err != nil || !fi.ModTime().Equal(s.mtime) || fi.Size() != s.size
		s.mu.Unlock()
		if !changed {
			continue
		}
		v, perr := readSecretFile(s.path)
		if err != nil || perr != nil {
			s.log.Error("secret_reload_failed", F{"key": s.key})
			s.metrics.Inc("be_secret_reload_failures_total", s.key)
			continue
		}
		s.mu.Lock()
		s.value, s.mtime, s.size = v, fi.ModTime(), fi.Size()
		s.mu.Unlock()
		s.log.Info("secret_reloaded", F{"key": s.key})
	}
}

// probeCapabilities is the capability part of P10.7: PostgreSQL 14 or later (declarative
// partitioning and FOR UPDATE SKIP LOCKED come with it).
func (d *DB) probeCapabilities(ctx context.Context) error {
	var num string
	err := d.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, d.Q("SELECT current_setting('server_version_num')")).Scan(&num)
	})
	if err != nil {
		return err
	}
	n, _ := strconv.Atoi(num)
	d.mu.Lock()
	d.pgMajor = n / 10000
	d.mu.Unlock()
	if n < 140000 {
		return &fatalError{fmt.Sprintf("PostgreSQL %s lacks a required capability: server_version_num >= 140000", num)}
	}
	return nil
}

type fatalError struct{ msg string }

func (e *fatalError) Error() string { return e.msg }

// unreachable: the connection could not be made or was lost.
func unreachable(err error) bool {
	var ce *pgconn.ConnectError
	var ne net.Error
	return errors.As(err, &ce) || errors.As(err, &ne) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		strings.Contains(err.Error(), "conn closed")
}
