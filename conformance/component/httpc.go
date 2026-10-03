package compconf

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// exchange is one HTTP request the suite made to the component and what came back.
type exchange struct {
	Method, Path string // Path without the query
	ReqHeader    http.Header
	Status       int
	Header       http.Header
	Body         []byte
	Problem      map[string]any // the parsed body of a 4xx/5xx, nil otherwise
	Err          error
	Took         time.Duration
}

func (x *exchange) parseProblem() {
	if x.Status < 400 {
		return
	}
	var m map[string]any
	if json.Unmarshal(x.Body, &m) == nil {
		x.Problem = m
	}
}

// reason is the problem's reason ("" when there is none).
func (x *exchange) reason() string {
	s, _ := x.Problem["reason"].(string)
	return s
}

func (x *exchange) str(k string) string {
	s, _ := x.Problem[k].(string)
	return s
}

// authorized reports that the request passed authentication and the route guard: it was not
// refused with 401, 403 or 503 AUTHZ_NOT_READY (a 404 or 400 from the handler is fine).
func (x *exchange) authorized() bool {
	if x.Err != nil {
		return false
	}
	switch x.Status {
	case 401, 403:
		return false
	case 503:
		return x.reason() != "AUTHZ_NOT_READY"
	}
	return true
}

// recorder keeps every exchange for the err profile.
type recorder struct {
	mu  sync.Mutex
	all []*exchange
}

func (r *recorder) add(x *exchange) { r.mu.Lock(); r.all = append(r.all, x); r.mu.Unlock() }

func (r *recorder) list() []*exchange {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*exchange(nil), r.all...)
}

// reqOpt shapes one request.
type reqOpt func(*http.Request, *[]byte)

func withToken(tok string) reqOpt {
	return func(r *http.Request, _ *[]byte) { r.Header.Set("Authorization", "Bearer "+tok) }
}

func withHeader(k, v string) reqOpt { return func(r *http.Request, _ *[]byte) { r.Header.Set(k, v) } }

func withBody(b []byte) reqOpt {
	return func(r *http.Request, body *[]byte) {
		*body = b
		r.Header.Set("Content-Type", "application/json")
	}
}

var httpClient = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}

// send makes one request to base+target (target may carry a query) and records it.
func send(ctx context.Context, rec *recorder, base, method, target string, opts ...reqOpt) *exchange {
	req, err := http.NewRequestWithContext(ctx, method, base+target, nil)
	x := &exchange{Method: method, Path: strings.SplitN(target, "?", 2)[0]}
	if err != nil {
		x.Err = err
		return x
	}
	var body []byte
	for _, o := range opts {
		o(req, &body)
	}
	if body != nil {
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
	}
	x.ReqHeader = req.Header.Clone()
	start := time.Now()
	resp, err := httpClient.Do(req)
	x.Took = time.Since(start)
	if err != nil {
		x.Err = err
		return x
	}
	defer resp.Body.Close()
	x.Status, x.Header = resp.StatusCode, resp.Header
	x.Body, _ = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	x.parseProblem()
	if rec != nil {
		rec.add(x)
	}
	return x
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
