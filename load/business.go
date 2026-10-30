package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Chain is a business flow through the trading components (step 7, T7.10): each iteration makes an order and takes it
// as far as the flow goes, one request after another, as a salesperson and then a billing clerk would. The rate is
// iterations per second; the iteration's latency runs from when it was due to its last answer.
type Chain struct {
	Flow  string // confirm: order and confirm; deliver: and deliver; bill: and draft and issue the invoice; print: and print it
	World World
	RunID string // the idempotency keys are load:<RunID>:<n>:<step>, so a rerun never replays an earlier one
}

// World is what the iterations need, from the cell (conformance: python -m cells world <cell> <file>): the user
// surfaces, and the seed data an order is made of. Customers and goods are taken in turn, so the load spreads over
// several credit limits and stock positions instead of queueing on one row.
type World struct {
	Sales       string   `json:"sales"`   // e.g. http://127.0.0.1:18084
	Billing     string   `json:"billing"` // e.g. http://127.0.0.1:18089
	LegalEntity string   `json:"legal_entity_id"`
	Currency    string   `json:"currency"`
	Owner       string   `json:"owner_id"`
	Customers   []string `json:"customers"`
	Goods       []Item   `json:"goods"`
}

// Item is a product an order line can take: in stock in the warehouse, priced in the world's currency.
type Item struct {
	Product   string `json:"product_id"`
	Unit      string `json:"unit"`
	Warehouse string `json:"warehouse_id"`
}

// Step is one request of an iteration.
type Step struct {
	Name    string        `json:"name"`
	Service time.Duration `json:"-"`
	Status  int           `json:"status"`
	Error   string        `json:"error,omitempty"`
}

var flows = map[string][]string{
	"confirm": {"order", "confirm"},
	"deliver": {"order", "confirm", "deliver"},
	"bill":    {"order", "confirm", "deliver", "draft", "issue"},
	"print":   {"order", "confirm", "deliver", "draft", "issue", "print"}, // suite C, R3: a burst of billing and printing
}

func readWorld(path string) (World, error) {
	var w World
	raw, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(raw, &w)
	}
	if err == nil && (len(w.Customers) == 0 || len(w.Goods) == 0) {
		err = fmt.Errorf("%s has no customers or no goods", path)
	}
	return w, err
}

// run is iteration n, due at due; its sample carries every step it got to.
func (c *Chain) run(ctx context.Context, client *http.Client, due time.Time, n int, timeout time.Duration) Sample {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	w, s := c.World, Sample{Due: due}
	item, customer := w.Goods[n%len(w.Goods)], w.Customers[n%len(w.Customers)]
	var order, line, invoice string
	started := time.Now()
	for _, name := range flows[c.Flow] {
		var url string
		var body any
		switch name {
		case "order":
			url, body = w.Sales+"/api/sales/orders", map[string]any{"legal_entity_id": w.LegalEntity, "customer_id": customer,
				"currency": w.Currency, "owner_id": w.Owner, "lines": []map[string]any{{"product_id": item.Product,
					"quantity": "1", "unit": item.Unit, "warehouse_id": item.Warehouse, "tax_rate": "0.13"}}}
		case "confirm":
			url = w.Sales + "/api/sales/orders/" + order + "/confirm"
		case "deliver":
			url, body = w.Sales+"/api/sales/deliveries", map[string]any{"order_id": order,
				"lines": []map[string]any{{"order_line_id": line, "quantity": "1"}}}
		case "draft":
			url, body = w.Billing+"/api/billing/invoices", map[string]any{"order_id": order}
		case "issue":
			url = w.Billing + "/api/billing/invoices/" + invoice + "/issue"
		case "print":
			url = w.Billing + "/api/billing/invoices/" + invoice + "/print"
		}
		st, out := post(ctx, client, url, fmt.Sprintf("load:%s:%d:%s", c.RunID, n, name), body)
		st.Name = name
		if st.Error == "" && name == "confirm" && out["outcome"] != "confirmed" {
			st.Error = fmt.Sprintf("%v %v", out["outcome"], out["reason"]) // refused or still processing: not a success
		}
		s.Steps = append(s.Steps, st)
		s.Status = st.Status
		if st.Error != "" {
			s.Error = name + ": " + st.Error
			break
		}
		switch name {
		case "order":
			order, _ = out["id"].(string)
			if lines, _ := out["lines"].([]any); len(lines) > 0 {
				line, _ = lines[0].(map[string]any)["id"].(string)
			}
			s.ID = order
		case "draft":
			invoice, _ = out["id"].(string)
		}
	}
	s.Latency, s.Service, s.Done = time.Since(due), time.Since(started), time.Now()
	return s
}

func post(ctx context.Context, client *http.Client, url, key string, body any) (Step, map[string]any) {
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	if b := bearer.Load().(string); b != "" {
		req.Header.Set("Authorization", b)
	}
	sent := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return Step{Service: time.Since(sent), Error: err.Error()}, nil
	}
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	st := Step{Service: time.Since(sent), Status: resp.StatusCode}
	var out map[string]any
	_ = json.Unmarshal(answer, &out)
	if resp.StatusCode >= 300 {
		st.Error = strings.TrimSpace(fmt.Sprintf("%d %v", resp.StatusCode, out["reason"]))
	}
	return st, out
}

// StepSummary is one step's requests over a run.
type StepSummary struct {
	Sent    int            `json:"sent"`
	OK      int            `json:"ok"`
	Errors  map[string]int `json:"errors,omitempty"`
	Service Percentiles    `json:"service_ms"`
}

func stepSummaries(samples []Sample) map[string]StepSummary {
	out := map[string]StepSummary{}
	svc := map[string][]time.Duration{}
	for _, x := range samples {
		for _, st := range x.Steps {
			s := out[st.Name]
			s.Sent++
			if st.Error == "" {
				s.OK++
				svc[st.Name] = append(svc[st.Name], st.Service)
			} else {
				if s.Errors == nil {
					s.Errors = map[string]int{}
				}
				s.Errors[st.Error]++
			}
			out[st.Name] = s
		}
	}
	for name, s := range out {
		s.Service = percentiles(svc[name])
		out[name] = s
	}
	return out
}
