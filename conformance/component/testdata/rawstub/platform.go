package main

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// platformDDL is be-protocol's reference DDL (platform/SOURCE names the tag), applied verbatim.
//
//go:embed platform/*.sql
var platformDDL embed.FS

// platformVersion is the version recorded in besdk_platform_version.
const platformVersion = 1

// ownsResourceTypes: ddl/07 (the ACL projection) is created only by an owner of resource types;
// the stub declares conformance.rawstub.note (it keeps no projection rows: no relations).
const ownsResourceTypes = true

// applyPlatform is the platform migration of P11.3, after the component's own migrations, as
// the owner: the besdk_* tables and functions, the version row, then the current partition
// window of the partitioned platform table besdk_outbox (P16.6).
func applyPlatform(ctx context.Context, conn *pgx.Conn, cfg *Config) error {
	files, _ := fs.Glob(platformDDL, "platform/*.sql")
	sort.Strings(files)
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		for _, f := range files {
			if strings.Contains(f, "07-authz") && !ownsResourceTypes {
				continue
			}
			b, _ := platformDDL.ReadFile(f)
			if _, err := tx.Exec(ctx, string(b), pgx.QueryExecModeSimpleProtocol); err != nil {
				return fmt.Errorf("%s: %w", f, err)
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO besdk_platform_version (component, version) VALUES ($1, $2)
			ON CONFLICT (component) DO UPDATE SET version = EXCLUDED.version, applied_at = now()
			WHERE besdk_platform_version.version < EXCLUDED.version`, cfg.ComponentID, platformVersion); err != nil {
			return err
		}
		return ensureOutboxWindow(ctx, tx, "", time.Now())
	})
}

// outboxAhead is how many ISO weeks beyond the current one besdk_outbox keeps ready.
const outboxAhead = 1

// weekStart is Monday 00:00 UTC of t's ISO week.
func weekStart(t time.Time) time.Time {
	t = t.UTC()
	d := (int(t.Weekday()) + 6) % 7
	return time.Date(t.Year(), t.Month(), t.Day()-d, 0, 0, 0, 0, time.UTC)
}

// outboxPartition names the ISO week of t: besdk_outbox_<isoyear>w<ww>.
func outboxPartition(t time.Time) string {
	y, w := t.UTC().ISOWeek()
	return fmt.Sprintf("besdk_outbox_%dw%02d", y, w)
}

// ensureOutboxWindow creates the current week's partition and outboxAhead more through the
// platform function besdk_ensure_range_partition (idempotent; usable by the runtime role).
func ensureOutboxWindow(ctx context.Context, tx pgx.Tx, prefix string, now time.Time) error {
	start := weekStart(now)
	for i := 0; i <= outboxAhead; i++ {
		from := start.AddDate(0, 0, 7*i)
		if _, err := tx.Exec(ctx, prefix+"SELECT besdk_ensure_range_partition('besdk_outbox', $1, $2, $3)",
			outboxPartition(from), from, from.AddDate(0, 0, 7)); err != nil {
			return err
		}
	}
	return nil
}

func platformApplied(ctx context.Context, d *DB) (bool, error) {
	var ok bool
	err := d.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, d.Q(`SELECT to_regclass('besdk_platform_version') IS NOT NULL
			AND EXISTS (SELECT 1 FROM besdk_platform_version WHERE version >= `+fmt.Sprint(platformVersion)+`)`)).Scan(&ok)
	})
	return ok, err
}
