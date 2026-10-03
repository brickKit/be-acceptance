package fakes

import (
	"net/http"
	"strings"
	"time"
)

// serveHTTP answers the user-plane operations keyed "<METHOD> <path>"; a {param} segment
// matches any one segment.
func (p *Peer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	route, a, found := "", PeerAnswer{}, false
	for k, v := range p.answers {
		if matchRoute(k, r.Method, r.URL.Path) {
			route, a, found = k, v, true
			break
		}
	}
	if found {
		p.http = append(p.http, HTTPCall{Route: route, Path: r.URL.Path, Header: r.Header.Clone(), At: time.Now()})
	}
	rel := p.release
	p.mu.Unlock()
	if !found {
		http.NotFound(w, r)
		return
	}
	if a.Hang || a.Delay > 0 {
		var timer <-chan time.Time
		if !a.Hang {
			timer = time.After(a.Delay)
		}
		select {
		case <-r.Context().Done():
			return
		case <-rel:
		case <-timer:
		}
	}
	if a.Code != 0 {
		writeJSON(w, httpStatusOf(int(a.Code)), map[string]any{"reason": a.Reason, "domain": a.Domain})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if len(a.Response) == 0 {
		a.Response = []byte("{}")
	}
	_, _ = w.Write(a.Response)
}

func matchRoute(route, method, path string) bool {
	m, tmpl, ok := strings.Cut(route, " ")
	if !ok || m != method {
		return false
	}
	ts, ps := strings.Split(tmpl, "/"), strings.Split(path, "/")
	if len(ts) != len(ps) {
		return false
	}
	for i := range ts {
		if strings.HasPrefix(ts[i], "{") && strings.HasSuffix(ts[i], "}") && ps[i] != "" {
			continue
		}
		if ts[i] != ps[i] {
			return false
		}
	}
	return true
}

// httpStatusOf maps a gRPC code number to its HTTP status (be-protocol P4, code mapping).
func httpStatusOf(code int) int {
	switch code {
	case 3, 9, 11:
		return 400
	case 16:
		return 401
	case 7:
		return 403
	case 5:
		return 404
	case 6, 10:
		return 409
	case 8:
		return 429
	case 1:
		return 499
	case 12:
		return 501
	case 14:
		return 503
	case 4:
		return 504
	}
	return 500
}
