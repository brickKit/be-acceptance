package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// One-step idempotent commands (P13, P3.7): the key is claimed in the same transaction as the
// write, so a replay, a mismatch and a concurrent duplicate are all decided by the row in
// besdk_idempotency.

// idemKey reads the key from the header Idempotency-Key or the body field idempotency_key;
// both with different values is IDEMPOTENCY_MISMATCH. "" = the request carries none.
func idemKey(rc *reqCtx, body []byte) (string, error) {
	h := rc.r.Header.Get("Idempotency-Key")
	var in struct {
		Key *string `json:"idempotency_key"`
	}
	_ = json.Unmarshal(body, &in)
	switch {
	case in.Key != nil && h != "" && *in.Key != h:
		return "", beErr("IDEMPOTENCY_MISMATCH", nil)
	case in.Key != nil:
		return *in.Key, nil
	}
	return h, nil
}

// fingerprint is SHA-256 of the RFC 8785 form of the declared fields. The stub's fingerprint
// fields are strings, whose JCS form is Go's JSON encoding without HTML escaping, keys sorted.
func fingerprint(fields map[string]string) []byte {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(jcsString(k))
		b.WriteByte(':')
		b.Write(jcsString(fields[k]))
	}
	b.WriteByte('}')
	h := sha256.Sum256(b.Bytes())
	return h[:]
}

func jcsString(s string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return bytes.TrimRight(b.Bytes(), "\n")
}

// idemCall identifies one command for P13.2.
type idemCall struct {
	caller, key, command, target string
	hash                         []byte
}

// stored is a completed result.
type stored struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

// runIdempotent claims the key, runs exec in the same transaction unless the key is already
// done (replay) and stores the result. exec returns the status and body to answer.
func (a *App) runIdempotent(ctx context.Context, c idemCall, exec func(pgx.Tx) (int, any, error)) (int, json.RawMessage, error) {
	var out stored
	err := a.db.Tx(ctx, func(tx pgx.Tx) error {
		if broken == "idem-select-claim" { // a plain SELECT, then an INSERT: not atomic (P10.9)
			var n int
			if err := tx.QueryRow(ctx, a.db.Q(`SELECT count(*) FROM besdk_idempotency WHERE caller = $1 AND idempotency_key = $2`), c.caller, c.key).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return a.replay(ctx, tx, c, &out)
			}
			time.Sleep(50 * time.Millisecond)
		}
		var status string
		err := tx.QueryRow(ctx, a.db.Q(`INSERT INTO besdk_idempotency (caller, idempotency_key, command, target, request_hash, status, expires_at)
			VALUES ($1, $2, $3, $4, $5, 'CLAIMED', now() + interval '30 days')
			`+onConflict()+` RETURNING status`), c.caller, c.key, c.command, c.target, c.hash).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return a.replay(ctx, tx, c, &out)
		}
		if err != nil {
			return err
		}
		st, body, err := exec(tx)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(body)
		out = stored{Status: st, Body: raw}
		res, _ := json.Marshal(out)
		_, err = tx.Exec(ctx, a.db.Q(`UPDATE besdk_idempotency SET status = 'DONE', result = $3, updated_at = now()
			WHERE caller = $1 AND idempotency_key = $2`), c.caller, c.key, res)
		return err
	})
	return out.Status, out.Body, err
}

// replay compares the stored binding and returns its result (P13.2, P13.3).
func (a *App) replay(ctx context.Context, tx pgx.Tx, c idemCall, out *stored) error {
	var command, target, status string
	var hash []byte
	var result []byte
	err := tx.QueryRow(ctx, a.db.Q(`SELECT command, target, request_hash, status, result FROM besdk_idempotency
		WHERE caller = $1 AND idempotency_key = $2 FOR UPDATE`), c.caller, c.key).Scan(&command, &target, &hash, &status, &result)
	if err != nil {
		return err
	}
	if command != c.command || target != c.target || !bytes.Equal(hash, c.hash) {
		return beErr("IDEMPOTENCY_MISMATCH", nil)
	}
	if status != "DONE" {
		return beErr("IDEMPOTENCY_IN_PROGRESS", nil)
	}
	return json.Unmarshal(result, out)
}

// writeStored answers a (possibly replayed) result.
func writeStored(rc *reqCtx, status int, body json.RawMessage) error {
	rc.w.Header().Set("Content-Type", "application/json")
	rc.w.Header().Set("Content-Length", strconv.Itoa(len(body)+1))
	rc.w.WriteHeader(status)
	_, err := rc.w.Write(append(body, '\n'))
	return err
}

func onConflict() string {
	if broken == "idem-select-claim" {
		return ""
	}
	return "ON CONFLICT (caller, idempotency_key) DO NOTHING"
}
