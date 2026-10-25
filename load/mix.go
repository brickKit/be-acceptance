package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

// Mix is the experiment plan's representative load (step 8, T8.3): an open loop of operations in fixed shares, each
// done as one of the virtual users. A user acts in its own account's scope (the world file says which: a salesperson
// of one department, one over two departments, one abroad, the seed user over everything); billing and printing are
// the clerk's. Orders flow through pools — an order op leaves a draft, a confirm takes one and leaves a confirmed
// order, a deliver leaves a delivered one, a bill drafts, issues and prints its invoice — so the shares are also a
// funnel. When a pool is empty the op makes an order instead and the fallback is counted.
type Mix struct {
	World    MixWorld
	Users    int // virtual users; user n acts as World.Users[n % len]
	RunID    string
	sessions map[string]*session
	mu       sync.Mutex
	drafts   []pooled
	confirms []pooled
	delivers []pooled
	recent   []string // order IDs the list op reads
}

// MixWorld is written by conformance: python -m cells baseline <cell> <file>.
type MixWorld struct {
	Sales    string    `json:"sales"`
	Billing  string    `json:"billing"`
	TokenURL string    `json:"token_url"`
	Users    []MixUser `json:"users"`
	Clerk    string    `json:"clerk"`
}

// MixUser is one account's scope and what its orders are made of.
type MixUser struct {
	Account     string   `json:"account"`
	LegalEntity string   `json:"legal_entity_id"`
	Currency    string   `json:"currency"`
	Owner       string   `json:"owner_id"`
	Department  *string  `json:"department_id"`
	Warehouse   string   `json:"warehouse_id"`
	Customers   []string `json:"customers"`
	Goods       []string `json:"goods"`
	Unit        string   `json:"unit"`
	TaxRate     string   `json:"tax_rate"`
}

type pooled struct {
	order string
	lines []string
	user  int // index into World.Users
}

// mixShares are the plan's shares in percent: lists and reads 40, quotation to order 25, confirm 20, deliver 10,
// bill (draft, issue, print) 5.
var mixShares = map[string]int{"list": 40, "order": 25, "confirm": 20, "deliver": 10, "bill": 5}

// opWheel spreads every hundred iterations over the ops in their shares, interleaved (not 40 lists in a row).
var opWheel = func() []string {
	order := []string{"list", "order", "confirm", "deliver", "bill"}
	wheel, left := make([]string, 0, 100), map[string]int{}
	for k, v := range mixShares {
		left[k] = v
	}
	credit := map[string]int{}
	for len(wheel) < 100 {
		best := ""
		for _, op := range order { // smooth weighted round robin
			credit[op] += mixShares[op]
			if left[op] > 0 && (best == "" || credit[op] > credit[best]) {
				best = op
			}
		}
		credit[best] -= 100
		left[best]--
		wheel = append(wheel, best)
	}
	return wheel
}()

func pickOp(n int) string { return opWheel[n%100] }

// session is one account's token, renewed before it runs out.
type session struct {
	mu    sync.Mutex
	token string
	at    time.Time
	creds credentials
}

func (s *session) bearer(ctx context.Context, client *http.Client) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.at) < RefreshEvery && s.token != "" {
		return s.token, nil
	}
	t, err := fetchToken(ctx, client, s.creds)
	if err != nil {
		if s.token != "" {
			return s.token, nil // keep the current one; it lives a while longer
		}
		return "", err
	}
	s.token, s.at = "Bearer "+t, time.Now()
	return s.token, nil
}

// NewMix signs every account in once, so a wrong password fails the run before it starts.
func NewMix(ctx context.Context, client *http.Client, w MixWorld, users int, password, runID string) (*Mix, error) {
	if len(w.Users) == 0 || w.Clerk == "" {
		return nil, fmt.Errorf("the world has no users or no clerk")
	}
	m := &Mix{World: w, Users: users, RunID: runID, sessions: map[string]*session{}}
	accounts := []string{w.Clerk}
	for _, u := range w.Users {
		accounts = append(accounts, u.Account)
	}
	for _, a := range accounts {
		if m.sessions[a] != nil {
			continue
		}
		s := &session{creds: credentials{TokenURL: w.TokenURL, Username: a, Password: password}}
		if _, err := s.bearer(ctx, client); err != nil {
			return nil, fmt.Errorf("%s: %w", a, err)
		}
		m.sessions[a] = s
	}
	return m, nil
}

func readMixWorld(path string) (MixWorld, error) {
	var w MixWorld
	raw, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(raw, &w)
	}
	return w, err
}

func (m *Mix) take(pool *[]pooled) (pooled, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(*pool) == 0 {
		return pooled{}, false
	}
	x := (*pool)[0]
	*pool = (*pool)[1:]
	return x, true
}

func (m *Mix) put(pool *[]pooled, x pooled) {
	m.mu.Lock()
	defer m.mu.Unlock()
	*pool = append(*pool, x)
}

// run is iteration n, due at due: the op the wheel says, as virtual user n % Users.
func (m *Mix) run(ctx context.Context, client *http.Client, due time.Time, n int, timeout time.Duration) Sample {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	s := Sample{Due: due}
	op := pickOp(n)
	var x pooled
	var ok bool
	switch op {
	case "confirm":
		x, ok = m.take(&m.drafts)
	case "deliver":
		x, ok = m.take(&m.confirms)
	case "bill":
		x, ok = m.take(&m.delivers)
	default:
		ok = true
	}
	if !ok {
		s.Fallback, op = op, "order"
	}
	s.Op = op
	started := time.Now()
	u := n % m.Users % len(m.World.Users)
	if op == "confirm" || op == "deliver" {
		u = x.user // the order's own user: it is in that user's scope
	}
	user := m.World.Users[u]
	who := user.Account
	if op == "bill" {
		who = m.World.Clerk
	}
	auth, err := m.sessions[who].bearer(ctx, client)
	if err != nil {
		s.Error = "token: " + err.Error()
		s.Latency, s.Service, s.Done = time.Since(due), time.Since(started), time.Now()
		return s
	}
	key := func(step string) string { return fmt.Sprintf("mix:%s:%d:%s", m.RunID, n, step) }
	step := func(method, url, name string, body any) map[string]any {
		if s.Error != "" {
			return nil
		}
		st, out := request(ctx, client, method, url, key(name), auth, body)
		st.Name = op + "." + name
		s.Steps, s.Status = append(s.Steps, st), st.Status
		if st.Error != "" {
			s.Error = st.Name + ": " + st.Error
		}
		return out
	}
	switch op {
	case "list":
		m.mu.Lock()
		recent := ""
		if len(m.recent) > 0 {
			recent = m.recent[n%len(m.recent)]
		}
		m.mu.Unlock()
		switch {
		case n%4 == 0:
			step("GET", m.World.Sales+"/api/sales/orders?limit=50&legal_entity_id="+user.LegalEntity, "orders", nil)
		case n%4 == 1 && recent != "":
			step("GET", m.World.Sales+"/api/sales/orders/"+recent, "order", nil)
		case n%4 == 2:
			step("GET", m.World.Billing+"/api/billing/invoices?limit=50&legal_entity_id="+user.LegalEntity, "invoices", nil)
		default:
			step("GET", m.World.Sales+"/api/sales/deliveries?limit=50", "deliveries", nil)
		}
	case "order":
		lines := []map[string]any{}
		for i := 0; i < 1+n%3; i++ {
			lines = append(lines, map[string]any{"product_id": user.Goods[(n*7+i*131)%len(user.Goods)], "quantity": "1",
				"unit": user.Unit, "warehouse_id": user.Warehouse, "tax_rate": user.TaxRate})
		}
		body := map[string]any{"legal_entity_id": user.LegalEntity, "customer_id": user.Customers[n%len(user.Customers)],
			"currency": user.Currency, "owner_id": user.Owner, "lines": lines}
		if user.Department != nil {
			body["department_id"] = *user.Department
		}
		q := step("POST", m.World.Sales+"/api/sales/quotations", "quote", body)
		id, _ := q["id"].(string)
		step("POST", m.World.Sales+"/api/sales/quotations/"+id+"/send", "send", nil)
		a := step("POST", m.World.Sales+"/api/sales/quotations/"+id+"/accept", "accept", nil)
		order, _ := a["order_id"].(string)
		o := step("GET", m.World.Sales+"/api/sales/orders/"+order, "read", nil)
		if s.Error == "" {
			p := pooled{order: order, user: u}
			if ls, _ := o["lines"].([]any); ls != nil {
				for _, l := range ls {
					id, _ := l.(map[string]any)["id"].(string)
					p.lines = append(p.lines, id)
				}
			}
			m.put(&m.drafts, p)
			m.mu.Lock()
			if m.recent = append(m.recent, order); len(m.recent) > 200 {
				m.recent = m.recent[1:]
			}
			m.mu.Unlock()
		}
	case "confirm":
		out := step("POST", m.World.Sales+"/api/sales/orders/"+x.order+"/confirm", "confirm", nil)
		if s.Error == "" {
			if outcome, _ := out["outcome"].(string); outcome != "confirmed" {
				s.Outcome = outcome
				if outcome != "processing" { // refused: an error; processing: counted apart (the plan's “处理中”)
					s.Error = fmt.Sprintf("confirm: %v %v", outcome, out["reason"])
				}
			} else {
				m.put(&m.confirms, x)
			}
		}
	case "deliver":
		lines := []map[string]any{}
		for _, l := range x.lines {
			lines = append(lines, map[string]any{"order_line_id": l, "quantity": "1"})
		}
		step("POST", m.World.Sales+"/api/sales/deliveries", "deliver", map[string]any{"order_id": x.order, "lines": lines})
		if s.Error == "" {
			m.put(&m.delivers, x)
		}
	case "bill":
		inv := step("POST", m.World.Billing+"/api/billing/invoices", "draft", map[string]any{"order_id": x.order})
		id, _ := inv["id"].(string)
		step("POST", m.World.Billing+"/api/billing/invoices/"+id+"/issue", "issue", nil)
		step("POST", m.World.Billing+"/api/billing/invoices/"+id+"/print", "print", nil)
		if s.Error == "" {
			s.Billed = x.order
		}
	}
	s.Latency, s.Service, s.Done = time.Since(due), time.Since(started), time.Now()
	return s
}

// request is one call with the user's token; answers that are not JSON (a printed PDF) are read and dropped.
func request(ctx context.Context, client *http.Client, method, url, key, auth string, body any) (Step, map[string]any) {
	var raw io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		raw = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(ctx, method, url, raw)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", auth)
	if method != http.MethodGet {
		req.Header.Set("Idempotency-Key", key)
	}
	sent := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return Step{Service: time.Since(sent), Error: err.Error()}, nil
	}
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	_ = resp.Body.Close()
	st := Step{Service: time.Since(sent), Status: resp.StatusCode}
	var out map[string]any
	_ = json.Unmarshal(answer, &out)
	if resp.StatusCode >= 300 {
		st.Error = fmt.Sprintf("%d %v", resp.StatusCode, out["reason"])
	}
	return st, out
}

// OpSummary is one op's iterations over a run.
type OpSummary struct {
	Sent    int            `json:"sent"`
	OK      int            `json:"ok"`
	Errors  map[string]int `json:"errors,omitempty"`
	Latency Percentiles    `json:"latency_ms"`
}

// MixSummary is what a mixed run adds to the summary.
type MixSummary struct {
	Users      int                  `json:"users"`
	Ops        map[string]OpSummary `json:"ops"`
	Fallbacks  map[string]int       `json:"fallbacks,omitempty"` // op wanted -> times its pool was empty
	Processing int                  `json:"confirm_processing"`  // confirmations answered “processing”
	// the orders whose invoices were issued: how long the source took to complete each after its invoice was issued is
	// read from the database afterwards (the time from confirmation to billing in a load test is the load's own queue)
	Billed []string `json:"billed_orders"`
}

func mixSummary(m *Mix, samples []Sample) *MixSummary {
	out := &MixSummary{Users: m.Users, Ops: map[string]OpSummary{}, Fallbacks: map[string]int{}}
	lat := map[string][]time.Duration{}
	for _, x := range samples {
		o := out.Ops[x.Op]
		o.Sent++
		if x.Fallback != "" {
			out.Fallbacks[x.Fallback]++
		}
		if x.Outcome == "processing" {
			out.Processing++
		}
		if x.Error == "" {
			o.OK++
			lat[x.Op] = append(lat[x.Op], x.Latency)
			if x.Billed != "" {
				out.Billed = append(out.Billed, x.Billed)
			}
		} else {
			if o.Errors == nil {
				o.Errors = map[string]int{}
			}
			o.Errors[x.Error]++
		}
		out.Ops[x.Op] = o
	}
	for op, o := range out.Ops {
		o.Latency = percentiles(lat[op])
		out.Ops[op] = o
	}
	return out
}
