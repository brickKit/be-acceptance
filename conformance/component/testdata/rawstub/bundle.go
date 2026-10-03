package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sync"
	"time"
)

// Bundle is the part of the authz/2 bundle this stub uses (contract-infra-authz
// schemas/bundle.schema.json). Unknown members are ignored (E1).
type Bundle struct {
	Contract      string                     `json:"contract"`
	Revision      string                     `json:"revision"`
	Capabilities  map[string]json.RawMessage `json:"capabilities"`
	Roles         map[string][]string        `json:"roles"`
	Grants        map[string]Grant           `json:"grants"`
	Profiles      map[string]Profile         `json:"profiles"`
	Delegations   []Delegation               `json:"delegations"`
	StaleSince    map[string]int64           `json:"stale_since"`
	RevokedGrants map[string]int64           `json:"revoked_grants"`
}

type Grant struct {
	FromTS       *int64              `json:"from_ts"`
	Until        *int64              `json:"until"`
	Levels       map[string]string   `json:"levels"`
	DefaultLevel string              `json:"default_level"`
	Values       map[string][]string `json:"values"`
}

type Profile struct {
	Keys   []string `json:"keys"`
	Fields []string `json:"fields"`
}

type Delegation struct {
	Mode   string   `json:"mode"`
	From   string   `json:"from"`
	To     string   `json:"to"`
	Keys   []string `json:"keys"`
	FromTS *int64   `json:"from_ts"`
	Until  *int64   `json:"until"`
}

var contractRe = regexp.MustCompile(`^authz/2\.(0|[1-9][0-9]*)$`)

// capability is true only for a JSON true (list_objects may be an object: true when present).
func (b *Bundle) capability(name string) bool {
	raw, ok := b.Capabilities[name]
	if !ok {
		return false
	}
	var v bool
	if json.Unmarshal(raw, &v) == nil {
		return v
	}
	var obj map[string]any
	return json.Unmarshal(raw, &obj) == nil
}

// BundleStore polls GET {AUTHZ_URL}/authz/v2/bundle (P6.1).
type BundleStore struct {
	url    string
	client *http.Client
	log    *Logger

	mu       sync.RWMutex
	cur      *Bundle
	etag     string
	loadedAt time.Time
}

func newBundleStore(authzURL string, log *Logger) *BundleStore {
	return &BundleStore{url: authzURL + "/authz/v2/bundle", client: &http.Client{Timeout: 3 * time.Second},
		log: log}
}

// Current returns the bundle held, or nil before the first one.
func (s *BundleStore) Current() *Bundle {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

// Age is be_authz_bundle_age_seconds: time since the last successful fetch (or 304).
func (s *BundleStore) Age() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cur == nil {
		return 0
	}
	return time.Since(s.loadedAt).Seconds()
}

// Run fetches until ctx ends: every 15 s once loaded; backoff 0.5 s → 15 s before that.
func (s *BundleStore) Run(ctx context.Context) {
	supervise(ctx, s.log, "authz_bundle", func(ctx context.Context) {
		backoff := 500 * time.Millisecond
		for {
			wait := 15 * time.Second
			if err := s.fetch(ctx); err != nil {
				s.log.Warn("authz_bundle_fetch_failed", F{"error": err.Error()})
				if s.Current() == nil {
					wait = backoff
					backoff = minDur(backoff*2, 15*time.Second)
				}
			}
			if !sleepCtx(ctx, wait) {
				return
			}
		}
	})
}

func (s *BundleStore) fetch(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	s.mu.RLock()
	if s.etag != "" && s.cur != nil {
		req.Header.Set("If-None-Match", s.etag)
	}
	s.mu.RUnlock()
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		s.mu.Lock()
		s.loadedAt = time.Now()
		s.mu.Unlock()
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bundle status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	var b Bundle
	if err := json.Unmarshal(body, &b); err != nil {
		return err
	}
	if !contractRe.MatchString(b.Contract) {
		s.log.Error("authz_bundle_refused", F{"error": fmt.Sprintf("contract %q is not authz/2.*", b.Contract)})
		return errors.New("bundle refused")
	}
	s.mu.Lock()
	first := s.cur == nil
	s.cur, s.etag, s.loadedAt = &b, resp.Header.Get("ETag"), time.Now()
	s.mu.Unlock()
	if first {
		s.log.Info("authz_bundle_loaded", F{"revision": b.Revision})
	}
	return nil
}

// tokenChecks is E2 (stale, revoked grant, delegation, act chain); reason "" when it passes.
func tokenChecks(b *Bundle, c *Claims) (reason, kind string) {
	if since, ok := b.StaleSince[c.Sub]; ok && c.Iat < since-5 {
		return "TOKEN_STALE", ""
	}
	if c.Dg != "" {
		if _, ok := b.RevokedGrants[c.Dg]; ok {
			return "TOKEN_STALE", ""
		}
	}
	delegated := c.Act != nil || len(c.Ceil) > 0 || c.Dg != ""
	if delegated && !b.capability("delegation") {
		k := "delegation"
		if c.Act != nil {
			k = c.Act.Kind
		}
		return "UNSUPPORTED_DELEGATION", k
	}
	for a := c.Act; a != nil; a = a.Act {
		if (a.Kind == "agent" && !b.capability("agents")) || (a.Kind == "user" && !b.capability("impersonation")) {
			return "UNSUPPORTED_DELEGATION", a.Kind
		}
	}
	return "", ""
}

func inWindow(from, until *int64, now int64) bool {
	return (from == nil || now >= *from) && (until == nil || now < *until)
}

// hasKey is E3–E5: an active role grants K, or an on_behalf delegation does, and every
// ceiling allows K.
func hasKey(b *Bundle, c *Claims, key string, now int64) bool {
	for _, code := range c.Ceil {
		p := b.Profiles[code] // unknown code = empty profile
		if !contains(p.Keys, key) && !contains(p.Fields, key) {
			return false
		}
	}
	for _, r := range c.Roles {
		if g, ok := b.Grants[r]; ok && !inWindow(g.FromTS, g.Until, now) {
			continue
		}
		if contains(b.Roles[r], key) {
			return true
		}
	}
	if b.capability("delegation") {
		for _, d := range b.Delegations {
			if d.Mode == "on_behalf" && d.To == c.Sub && contains(d.Keys, key) && inWindow(d.FromTS, d.Until, now) {
				return true
			}
		}
	}
	return false
}
