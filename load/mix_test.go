package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTheOpsFollowTheSharesExactlyOverEveryHundred(t *testing.T) {
	got := map[string]int{}
	for n := 0; n < 1000; n++ {
		got[pickOp(n)]++
	}
	for op, share := range mixShares {
		if got[op] != share*10 {
			t.Fatalf("%s: %d of 1000, want %d", op, got[op], share*10)
		}
	}
}

// mixStandIn answers the token exchange (token "tok-<user>") and every route the mix calls; it records which token
// each write came with, and the order of the steps each order went through.
type mixStandIn struct {
	mu      sync.Mutex
	writes  map[string]map[string]int // path kind -> token -> count
	journey map[string][]string       // order id -> steps
	quotes  int
}

func newMixStandIn(t *testing.T) (*mixStandIn, string) {
	s := &mixStandIn{writes: map[string]map[string]int{}, journey: map[string][]string{}}
	url := server(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		defer s.mu.Unlock()
		p, tok := r.URL.Path, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		note := func(kind string) {
			if s.writes[kind] == nil {
				s.writes[kind] = map[string]int{}
			}
			s.writes[kind][tok]++
		}
		parts := strings.Split(p, "/")
		switch {
		case p == "/token":
			json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-" + body["username"].(string)})
		case r.Method == http.MethodGet:
			if strings.HasPrefix(p, "/api/sales/orders/") {
				json.NewEncoder(w).Encode(map[string]any{"id": parts[4], "lines": []any{map[string]any{"id": "l-" + parts[4]}}})
				return
			}
			w.Write([]byte(`{"orders":[],"invoices":[],"deliveries":[]}`))
		case p == "/api/sales/quotations":
			note("quote")
			s.quotes++
			json.NewEncoder(w).Encode(map[string]any{"id": "q-" + r.Header.Get("Idempotency-Key")})
		case strings.HasSuffix(p, "/send"):
			w.Write([]byte(`{}`))
		case strings.HasSuffix(p, "/accept"):
			id := "o-" + parts[4]
			s.journey[id] = append(s.journey[id], "order")
			json.NewEncoder(w).Encode(map[string]any{"order_id": id})
		case strings.HasSuffix(p, "/confirm"):
			note("confirm")
			s.journey[parts[4]] = append(s.journey[parts[4]], "confirm")
			json.NewEncoder(w).Encode(map[string]any{"outcome": "confirmed"})
		case p == "/api/sales/deliveries":
			note("deliver")
			s.journey[body["order_id"].(string)] = append(s.journey[body["order_id"].(string)], "deliver")
			w.Write([]byte(`{}`))
		case p == "/api/billing/invoices":
			note("draft")
			json.NewEncoder(w).Encode(map[string]any{"id": "i-" + body["order_id"].(string)})
		case strings.HasSuffix(p, "/issue"):
			note("issue")
			s.journey[strings.TrimPrefix(parts[4], "i-")] = append(s.journey[strings.TrimPrefix(parts[4], "i-")], "issue")
			w.Write([]byte(`{}`))
		case strings.HasSuffix(p, "/print"):
			note("print")
			w.Header().Set("Content-Type", "application/pdf")
			w.Write([]byte("%PDF-1.7"))
		default:
			w.WriteHeader(404)
		}
	})
	return s, url
}

func mixWorld(url string) MixWorld {
	user := func(account string) MixUser {
		return MixUser{Account: account, LegalEntity: "le", Currency: "CNY", Owner: "owner-" + account, Warehouse: "wh",
			Customers: []string{"c1", "c2"}, Goods: []string{"g1", "g2", "g3"}, Unit: "C62", TaxRate: "0.13"}
	}
	return MixWorld{Sales: url, Billing: url, TokenURL: url + "/token", Clerk: "test.bob",
		Users: []MixUser{user("test.sales.cn"), user("test.sales.multi"), user("test.bob")}}
}

func runMix(t *testing.T, url string, rate float64, d time.Duration) Summary {
	m, err := NewMix(context.Background(), client(), mixWorld(url), 20, "pw", "t")
	if err != nil {
		t.Fatal(err)
	}
	p := Plan{Rate: rate, Warmup: d / 2, Duration: d, Mix: m}
	return summarize(p, Run(context.Background(), client(), p))
}

func TestOrdersFlowFromQuotationToPrintedInvoiceInOrder(t *testing.T) {
	s, url := newMixStandIn(t)
	sum := runMix(t, url, 200, 2*time.Second)
	if sum.OK != sum.Sent || sum.Sent < 350 {
		t.Fatalf("sent %d, ok %d, errors %v", sum.Sent, sum.OK, sum.Errors)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	billed := 0
	want := []string{"order", "confirm", "deliver", "issue"}
	for id, steps := range s.journey {
		for i, st := range steps {
			if st != want[i] {
				t.Fatalf("order %s went %v; every order goes order, confirm, deliver, issue, as far as it gets", id, steps)
			}
		}
		if len(steps) == 4 {
			billed++
		}
	}
	if billed == 0 || sum.Mix.Ops["bill"].OK == 0 || len(sum.Mix.Billed) != sum.Mix.Ops["bill"].OK {
		t.Fatalf("billed %d orders, mix %+v: the funnel never reached a printed invoice", billed, sum.Mix)
	}
}

func TestWritesCarryTheirUsersTokenAndBillingTheClerks(t *testing.T) {
	s, url := newMixStandIn(t)
	runMix(t, url, 200, 2*time.Second)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, kind := range []string{"quote", "confirm", "deliver"} {
		if len(s.writes[kind]) != 3 {
			t.Fatalf("%s came with tokens %v; want each of the three users' own", kind, s.writes[kind])
		}
	}
	for _, kind := range []string{"draft", "issue", "print"} {
		if len(s.writes[kind]) != 1 || s.writes[kind]["tok-test.bob"] == 0 {
			t.Fatalf("%s came with tokens %v; want only the clerk's", kind, s.writes[kind])
		}
	}
}

func TestTheSummaryCountsEveryOpAndItsShare(t *testing.T) {
	_, url := newMixStandIn(t)
	sum := runMix(t, url, 300, 2*time.Second)
	total := 0
	for _, o := range sum.Mix.Ops {
		total += o.Sent
	}
	if total != sum.Sent {
		t.Fatalf("ops add up to %d, sent %d", total, sum.Sent)
	}
	for op, share := range mixShares {
		got := 100 * float64(sum.Mix.Ops[op].Sent) / float64(sum.Sent)
		if got < float64(share)-2 || got > float64(share)+2 {
			t.Fatalf("%s: %.1f%% of the requests, want %d%% within 2 points (fallbacks %v)", op, got, share, sum.Mix.Fallbacks)
		}
	}
}
