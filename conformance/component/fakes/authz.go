package fakes

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
)

// Authz is fake-authz: an authorization provider speaking contract-infra-authz authz/2.0 on the
// provider plane: REST GET /authz/v2/bundle (ETag), /authz/v2/changes and /authz/v2/tuples (a
// gap-free changefeed of the tuples the suite or WriteTuples put in), and the gRPC service
// infra.authz.v2.AuthzProvider at AUTHZ_GRPC_URL (authz_grpc.go).
type Authz struct {
	mu       sync.Mutex
	roles    map[string][]string
	grants   map[string]RoleGrant
	stale    map[string]int64
	caps     map[string]any
	revision int64
	fetches  atomic.Int64
	tuples   map[string]TupleRec
	changes  []changeRec
	// changesReads counts GET /authz/v2/changes (the projection pull, P6.12).
	changesReads atomic.Int64
	grpc         *authzGRPC
	// OnChange, when set, is called after every change (the run publishes the poke).
	OnChange func()
}

// RoleGrant is a role's grant in the bundle (bundle.schema.json grants.<role>).
type RoleGrant struct {
	Levels       map[string]string   `json:"levels,omitempty"`
	DefaultLevel string              `json:"default_level,omitempty"`
	Values       map[string][]string `json:"values,omitempty"`
	Until        *int64              `json:"until,omitempty"`
}

// NewAuthz makes a provider offering only the core capability.
func NewAuthz() *Authz {
	return &Authz{
		roles: map[string][]string{}, grants: map[string]RoleGrant{}, stale: map[string]int64{}, tuples: map[string]TupleRec{},
		caps: map[string]any{
			"core": true, "admin_write": false, "sharing": false, "relation_sync": false, "check": false,
			"graph": false, "list_objects": false, "delegation": false, "agents": false,
			"impersonation": false, "access_review": false, "explain_paths": false, "conditions": false,
		},
		revision: 1,
	}
}

// update applies one change under the lock, bumps the revision and then calls OnChange.
func (f *Authz) update(apply func()) {
	f.mu.Lock()
	apply()
	f.revision++
	cb := f.OnChange
	f.mu.Unlock()
	if cb != nil {
		cb()
	}
}

// SetRole puts a role with its keys and grant into the bundle.
func (f *Authz) SetRole(code string, keys []string, g RoleGrant) {
	if keys == nil {
		keys = []string{}
	}
	f.update(func() { f.roles[code], f.grants[code] = keys, g })
}

// SetStale sets stale_since[sub] (seconds since the epoch).
func (f *Authz) SetStale(sub string, at int64) {
	f.update(func() { f.stale[sub] = at })
}

// SetCapability sets one capability value.
func (f *Authz) SetCapability(name string, v any) {
	f.update(func() { f.caps[name] = v })
}

// BundleFetches counts GET /authz/v2/bundle, including 304 answers.
func (f *Authz) BundleFetches() int64 { return f.fetches.Load() }

// Revision is the current revision.
func (f *Authz) Revision() int64 { f.mu.Lock(); defer f.mu.Unlock(); return f.revision }

func (f *Authz) bundle() (map[string]any, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rev := strconv.FormatInt(f.revision, 10)
	digest := sha256.Sum256([]byte("compconf-catalog"))
	return map[string]any{
		"contract":       "authz/2.0",
		"revision":       rev,
		"member":         map[string]string{"id": "conformance/fake-authz", "version": "1.0.0"},
		"capabilities":   copyMap(f.caps),
		"roles":          copyMap(f.roles),
		"grants":         copyMap(f.grants),
		"profiles":       map[string]any{},
		"delegations":    []any{},
		"stale_since":    copyMap(f.stale),
		"revoked_grants": map[string]any{},
		"catalog_digest": "sha256:" + hex.EncodeToString(digest[:]),
	}, `"` + rev + `"`
}

func copyMap[V any](m map[string]V) map[string]V {
	out := make(map[string]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Handler serves the provider plane.
func (f *Authz) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /authz/v2/bundle", func(w http.ResponseWriter, r *http.Request) {
		f.fetches.Add(1)
		b, etag := f.bundle()
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		writeJSON(w, http.StatusOK, b)
	})
	mux.HandleFunc("GET /authz/v2/changes", f.serveChanges)
	mux.HandleFunc("GET /authz/v2/tuples", f.serveTuples)
	return mux
}
