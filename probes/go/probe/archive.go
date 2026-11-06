package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/errs"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/tiering"
)

// The tiering profile (since 3.2.47, Go; assembly protocol 11): archives of every column type the canonical row knows,
// in three levels, closed (a terminal state) or open. The SDK seals, copies and releases them by its own jobs; the
// route is the deployment's choice (TIERING_ROUTE_PROBE_ARCHIVE: the warm route by default, or the object store).
// The retention is pending and nothing is erasable, so no hold or approval route: the profile needs no guard.

// ArchiveClass is the probe's data class.
const ArchiveClass = "probe.archive"

// Archive is the class.
func Archive() tiering.Class {
	return tiering.Class{Name: ArchiveClass, Category: tiering.CategoryCompleted,
		Root: tiering.Table{Name: "probe_archive", PK: "id", Columns: map[string]string{"id": tiering.TypeUUID,
			"batch": tiering.TypeText, "closed_at": tiering.TypeTimestamptz, "created_at": tiering.TypeTimestamptz,
			"t": tiering.TypeText, "i2": tiering.TypeInt2, "i4": tiering.TypeInt4, "i8": tiering.TypeInt8,
			"n": tiering.TypeNumeric, "b": tiering.TypeBool, "d": tiering.TypeDate, "j": tiering.TypeJSONB,
			"bin": tiering.TypeBytea, "tags": tiering.TypeTextArray}},
		Children: []tiering.Child{
			{Table: tiering.Table{Name: "probe_archive_part", PK: "id", Columns: map[string]string{"id": tiering.TypeUUID,
				"archive_id": tiering.TypeUUID, "label": tiering.TypeText}}, FK: "archive_id"},
			{Table: tiering.Table{Name: "probe_archive_piece", Key: []string{"part_id", "seq"}, Columns: map[string]string{
				"part_id": tiering.TypeUUID, "seq": tiering.TypeInt4, "note": tiering.TypeText}}, FK: "part_id",
				Parent: "probe_archive_part"}},
		Group: []string{"batch"}, Anchor: "closed_at", Created: "created_at", Terminal: "r.closed_at IS NOT NULL",
		ResidenceDays: 365, Retention: tiering.Retention{Pending: true}, Locate: []string{"batch"},
		Routes: []string{tiering.RoutePostgres, tiering.RouteObject}, SchemaVersion: "1"}
}

type archiveIn struct {
	Batch string `json:"batch"`
	// ClosedDaysAgo closes the archive that many days ago (it is due once past the residence); negative: open.
	ClosedDaysAgo int `json:"closed_days_ago"`
	Parts         int `json:"parts"`
}

// archiveHTTP makes one archive: a value of every type in its root (two of them null), its parts, two pieces each.
func (p *Probe) archiveHTTP(c *httpx.Context) error {
	var in archiveIn
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	if in.Batch == "" || len(in.Batch) > MaxLabel || in.Parts < 0 || in.Parts > 50 {
		return invalid("a batch of at most 64 characters and 0 to 50 parts")
	}
	id := ids.New().String()
	var closed any
	if in.ClosedDaysAgo >= 0 {
		closed = time.Now().Add(-time.Duration(in.ClosedDaysAgo) * 24 * time.Hour)
	}
	err := p.rt.DB.Tx(c.Ctx(), func(ctx context.Context, tx db.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO probe_archive (id, batch, closed_at, created_at, t, i2, i4, i8, n, b, d, j, bin, tags)
			VALUES ($1, $2, $3, now() - interval '500 days', 'café ünïcode', -32768, 2147483647, 9007199254740993, 1250.5,
			true, NULL, '{"a": [1, 2]}', '\x00ff10', ARRAY['B-2', 'A-1'])`, id, in.Batch, closed); err != nil {
			return err
		}
		for i := 0; i < in.Parts; i++ {
			part := ids.New().String()
			if _, err := tx.Exec(ctx, "INSERT INTO probe_archive_part VALUES ($1, $2, $3)", part, id, fmt.Sprintf("part %d", i)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "INSERT INTO probe_archive_piece VALUES ($1, 1, 'first'), ($1, 2, NULL)", part); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return c.JSON(200, map[string]string{"id": id})
}

// archiveRow answers one archive as canonical rows, wherever it is: "online", or the route it was archived on.
func (p *Probe) archiveRow(c *httpx.Context) error {
	id := c.Param("id")
	if p.rt.Tiering != nil {
		a, found, err := p.rt.Tiering.Get(c.Ctx(), ArchiveClass, id)
		if err != nil {
			return err
		}
		if found {
			return c.JSON(200, map[string]any{"where": a.Route, "root": a.Root, "children": sorted(a.Children)})
		}
	}
	cl := Archive()
	out := map[string]any{"where": "online"}
	children := map[string][]map[string]any{}
	err := p.rt.DB.Tx(c.Ctx(), func(ctx context.Context, tx db.Tx) error {
		roots, err := canonicalRows(ctx, tx, cl.Root, "t.id = $1::uuid", id)
		if err != nil || len(roots) == 0 {
			return err
		}
		out["root"] = roots[0]
		for _, ch := range cl.Children {
			where := "t.archive_id = $1::uuid"
			if ch.Parent != "" {
				where = "t.part_id IN (SELECT id FROM probe_archive_part WHERE archive_id = $1::uuid)"
			}
			if children[ch.Name], err = canonicalRows(ctx, tx, ch.Table, where, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if out["root"] == nil {
		return errs.New(errs.ReasonNotFound, "no such archive")
	}
	out["children"] = sorted(children)
	return c.JSON(200, out)
}

// canonicalRows reads rows of a class table as canonical rows (P15.8): what a route answers for them once archived.
func canonicalRows(ctx context.Context, tx db.Tx, t tiering.Table, where string, args ...any) ([]map[string]any, error) {
	names := make([]string, 0, len(t.Columns))
	for n := range t.Columns {
		names = append(names, n)
	}
	sort.Strings(names)
	exprs := make([]string, len(names))
	for i, n := range names {
		exprs[i] = "t." + n
		if typ := t.Columns[n]; typ == tiering.TypeNumeric || typ == tiering.TypeJSONB {
			exprs[i] += "::text"
		}
	}
	rows, err := tx.Query(ctx, fmt.Sprintf("SELECT %s FROM %s t WHERE %s", strings.Join(exprs, ", "), t.Name, where), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		m := map[string]any{}
		for i, n := range names {
			m[n] = vals[i]
		}
		b, err := tiering.Row(t.Columns, m)
		if err != nil {
			return nil, err
		}
		var obj map[string]any
		if err := json.Unmarshal(b, &obj); err != nil {
			return nil, err
		}
		out = append(out, obj)
	}
	return out, rows.Err()
}

// sorted orders each child table's rows by their canonical text, so an online read and an archived one compare equal.
func sorted(children map[string][]map[string]any) map[string][]map[string]any {
	for _, rows := range children {
		sort.Slice(rows, func(i, j int) bool {
			a, _ := json.Marshal(rows[i])
			b, _ := json.Marshal(rows[j])
			return string(a) < string(b)
		})
	}
	return children
}
