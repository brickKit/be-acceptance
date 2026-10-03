package fakes

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// TupleRec is one direct tuple the fake provider holds (changefeed.schema.json tuple).
type TupleRec struct {
	Type, ID, Relation, Subject string
	ExpiresAt                   *time.Time
}

func (t TupleRec) key() string { return t.Type + "\x00" + t.ID + "\x00" + t.Relation + "\x00" + t.Subject }

func (t TupleRec) wire() map[string]any {
	m := map[string]any{"object": map[string]string{"type": t.Type, "id": t.ID}, "relation": t.Relation, "subject": t.Subject}
	if t.ExpiresAt != nil {
		m["expires_at"] = t.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return m
}

// changeRec is one changefeed entry.
type changeRec struct {
	rev   int64
	op    string // upsert | delete
	tuple TupleRec
}

// PutTuple upserts a tuple at a new revision (a share written by the suite).
func (f *Authz) PutTuple(t TupleRec) string { return f.applyTuples([]TupleRec{t}, nil) }

// DeleteTuple deletes a tuple at a new revision.
func (f *Authz) DeleteTuple(t TupleRec) string { return f.applyTuples(nil, []TupleRec{t}) }

// applyTuples writes and deletes atomically at one new revision and returns it.
func (f *Authz) applyTuples(writes, deletes []TupleRec) string {
	var rev int64
	f.update(func() {
		rev = f.revision + 1 // update bumps the revision after apply
		for _, t := range writes {
			f.tuples[t.key()] = t
			f.changes = append(f.changes, changeRec{rev: rev, op: "upsert", tuple: t})
		}
		for _, t := range deletes {
			delete(f.tuples, t.key())
			f.changes = append(f.changes, changeRec{rev: rev, op: "delete", tuple: t})
		}
	})
	return strconv.FormatInt(rev, 10)
}

// Tuples returns the tuples of one type ("" = all), sorted.
func (f *Authz) Tuples(typ string) []TupleRec {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []TupleRec
	for _, t := range f.tuples {
		if typ == "" || t.Type == typ {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out
}

// readChanges is the changefeed page after a revision for some types.
func (f *Authz) readChanges(types []string, after int64, limit int) (out []changeRec, next, watermark string) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	watermark = strconv.FormatInt(f.revision, 10)
	next = watermark
	want := map[string]bool{}
	for _, t := range types {
		want[t] = true
	}
	for _, c := range f.changes {
		if c.rev <= after || !want[c.tuple.Type] {
			continue
		}
		if len(out) == limit {
			return out, strconv.FormatInt(out[len(out)-1].rev, 10), watermark
		}
		out = append(out, c)
	}
	return out, next, watermark
}

func (f *Authz) serveChanges(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	types := strings.Split(q.Get("types"), ",")
	f.changesReads.Add(1)
	cs, next, wm := f.readChanges(types, after, limit)
	items := make([]any, 0, len(cs))
	for _, c := range cs {
		items = append(items, map[string]any{"revision": strconv.FormatInt(c.rev, 10), "op": c.op, "tuple": c.tuple.wire()})
	}
	writeJSON(w, http.StatusOK, map[string]any{"changes": items, "next": next, "watermark": wm})
}

func (f *Authz) serveTuples(w http.ResponseWriter, r *http.Request) {
	items := []any{}
	for _, t := range f.Tuples(r.URL.Query().Get("type")) {
		items = append(items, t.wire())
	}
	writeJSON(w, http.StatusOK, map[string]any{"tuples": items, "next_cursor": "", "revision": strconv.FormatInt(f.Revision(), 10)})
}
