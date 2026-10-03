package main

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"
)

// jwk is one public signing key of the JWKS.
type jwk struct {
	alg string
	key crypto.PublicKey
}

// JWKS caches {IAM_URL}/.well-known/jwks.json (P5.4): at most 1 h old, an unknown kid
// triggers one refetch at most once per 30 s, each fetch times out after 3 s, and a failed
// fetch keeps the keys already held.
type JWKS struct {
	url    string
	client *http.Client
	log    *Logger

	mu          sync.Mutex
	keys        map[string]jwk
	fetchedAt   time.Time
	lastKidPull time.Time
	fetchMu     sync.Mutex
}

const (
	jwksMaxAge      = time.Hour
	jwksKidInterval = 30 * time.Second
)

func newJWKS(iamURL string, log *Logger) *JWKS {
	return &JWKS{url: iamURL + "/.well-known/jwks.json", client: &http.Client{Timeout: 3 * time.Second}, log: log}
}

// Run loads the keys in the background: backoff 0.5 s → 15 s until the first success, then
// a refresh every hour (P1.2, P5.4).
func (j *JWKS) Run(ctx context.Context) {
	supervise(ctx, j.log, "jwks", func(ctx context.Context) {
		backoff := 500 * time.Millisecond
		for {
			wait := jwksMaxAge
			if err := j.fetch(ctx); err != nil {
				j.log.Warn("jwks_fetch_failed", F{"error": err.Error()})
				if !j.loaded() {
					wait = backoff
					backoff = minDur(backoff*2, 15*time.Second)
				}
			} else {
				backoff = 500 * time.Millisecond
			}
			if !sleepCtx(ctx, wait) {
				return
			}
		}
	})
}

func (j *JWKS) loaded() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.keys != nil
}

// lookup returns the key for kid, refetching once (rate-limited) when the kid is unknown or
// the cache is older than an hour.
func (j *JWKS) lookup(ctx context.Context, kid string) (jwk, bool) {
	j.mu.Lock()
	k, ok := j.keys[kid]
	stale := time.Since(j.fetchedAt) > jwksMaxAge
	mayPull := time.Since(j.lastKidPull) >= jwksKidInterval
	if !ok && mayPull {
		j.lastKidPull = time.Now()
	}
	j.mu.Unlock()
	if ok && !stale {
		return k, true
	}
	if !ok && !mayPull {
		return jwk{}, false
	}
	if err := j.fetch(ctx); err != nil {
		j.log.Warn("jwks_fetch_failed", F{"error": err.Error()})
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	k, ok = j.keys[kid]
	return k, ok
}

func (j *JWKS) fetch(ctx context.Context) error {
	j.fetchMu.Lock()
	defer j.fetchMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, j.url, nil)
	resp, err := j.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	keys, err := parseJWKS(body)
	if err != nil {
		return err
	}
	j.mu.Lock()
	j.keys, j.fetchedAt = keys, time.Now()
	j.mu.Unlock()
	return nil
}

// parseJWKS keeps the RS256, ES256 (P-256) and EdDSA (Ed25519) keys; a key with a private
// member or an inconsistent kty/alg is skipped.
func parseJWKS(body []byte) (map[string]jwk, error) {
	var doc struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	out := map[string]jwk{}
	for _, k := range doc.Keys {
		kid, _ := k["kid"].(string)
		if kid == "" || hasPrivate(k) {
			continue
		}
		if pk, alg, err := publicKey(k); err == nil {
			out[kid] = jwk{alg: alg, key: pk}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("jwks holds no usable key")
	}
	return out, nil
}

func hasPrivate(k map[string]any) bool {
	for _, m := range []string{"d", "p", "q", "dp", "dq", "qi", "k"} {
		if _, ok := k[m]; ok {
			return true
		}
	}
	return false
}

func b64field(k map[string]any, name string) ([]byte, error) {
	s, _ := k[name].(string)
	if s == "" {
		return nil, fmt.Errorf("missing %s", name)
	}
	return base64.RawURLEncoding.DecodeString(s)
}

func publicKey(k map[string]any) (crypto.PublicKey, string, error) {
	kty, _ := k["kty"].(string)
	alg, _ := k["alg"].(string)
	crv, _ := k["crv"].(string)
	switch {
	case kty == "RSA" && alg == "RS256":
		n, err1 := b64field(k, "n")
		e, err2 := b64field(k, "e")
		if err := errors.Join(err1, err2); err != nil {
			return nil, "", err
		}
		ee := new(big.Int).SetBytes(e)
		if !ee.IsInt64() || ee.Int64() < 3 || ee.Int64() > 1<<31 {
			return nil, "", errors.New("bad exponent")
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(ee.Int64())}, alg, nil
	case kty == "EC" && alg == "ES256" && crv == "P-256":
		x, err1 := b64field(k, "x")
		y, err2 := b64field(k, "y")
		if err := errors.Join(err1, err2); err != nil || len(x) != 32 || len(y) != 32 {
			return nil, "", errors.New("bad EC key")
		}
		uncompressed := append(append([]byte{4}, x...), y...)
		pk, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), uncompressed)
		if err != nil {
			return nil, "", err
		}
		return pk, alg, nil
	case kty == "OKP" && alg == "EdDSA" && crv == "Ed25519":
		x, err := b64field(k, "x")
		if err != nil || len(x) != ed25519.PublicKeySize {
			return nil, "", errors.New("bad Ed25519 key")
		}
		return ed25519.PublicKey(x), alg, nil
	}
	return nil, "", errors.New("unsupported key")
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// sleepCtx waits d or until ctx ends; false when ctx ended.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// supervise runs background work, recovering a panic and restarting with backoff 1 s → 5 min
// (P1.7).
func supervise(ctx context.Context, log *Logger, name string, work func(context.Context)) {
	backoff := time.Second
	for ctx.Err() == nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Error("background_panic", F{"work": name, "error": fmt.Sprint(r)})
				}
			}()
			work(ctx)
		}()
		if !sleepCtx(ctx, backoff) {
			return
		}
		backoff = minDur(backoff*2, 5*time.Minute)
	}
}
