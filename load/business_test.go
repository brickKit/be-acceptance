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

// standIn answers the trading routes a chain calls, records every idempotency key and customer, and refuses the
// confirmation of every order of the customer "refused".
func standIn(t *testing.T) (string, func() (keys map[string]int, customers map[string]int)) {
	var mu sync.Mutex
	keys, customers, owner := map[string]int{}, map[string]int{}, map[string]string{}
	url := server(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		defer mu.Unlock()
		keys[r.Header.Get("Idempotency-Key")]++
		p := r.URL.Path
		switch {
		case p == "/api/sales/orders":
			c := body["customer_id"].(string)
			customers[c]++
			id := "o-" + r.Header.Get("Idempotency-Key")
			owner[id] = c
			json.NewEncoder(w).Encode(map[string]any{"id": id, "lines": []any{map[string]any{"id": "l-" + id}}})
		case strings.HasSuffix(p, "/confirm"):
			id := strings.Split(p, "/")[4]
			if owner[id] == "refused" {
				json.NewEncoder(w).Encode(map[string]any{"outcome": "rejected", "reason": "over_limit"})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"outcome": "confirmed"})
		case p == "/api/sales/deliveries", strings.HasSuffix(p, "/issue"):
			w.Write([]byte(`{}`))
		case p == "/api/billing/invoices":
			json.NewEncoder(w).Encode(map[string]any{"id": "i-" + body["order_id"].(string)})
		default:
			w.WriteHeader(404)
		}
	})
	return url, func() (map[string]int, map[string]int) {
		mu.Lock()
		defer mu.Unlock()
		return keys, customers
	}
}

func TestABillChainRunsEveryStepOnceWithItsOwnKeys(t *testing.T) {
	url, seen := standIn(t)
	w := World{Sales: url, Billing: url, LegalEntity: "le", Currency: "CNY", Owner: "me",
		Customers: []string{"a", "b"}, Goods: []Item{{"p", "C62", "wh"}}}
	p := Plan{Rate: 50, Duration: time.Second, Chain: &Chain{Flow: "bill", World: w, RunID: "t"}}
	s := summarize(p, Run(context.Background(), client(), p))
	if s.Sent < 48 || s.OK != s.Sent {
		t.Fatalf("sent %d, ok %d, errors %v; want about 50 chains, all through", s.Sent, s.OK, s.Errors)
	}
	for _, step := range flows["bill"] {
		if st := s.Steps[step]; st.Sent != s.Sent || st.OK != s.Sent {
			t.Fatalf("step %s: %+v; want %d sent and ok", step, st, s.Sent)
		}
	}
	keys, customers := seen()
	for k, n := range keys {
		if n != 1 {
			t.Fatalf("key %s sent %d times; every request has its own", k, n)
		}
	}
	if len(keys) != 5*s.Sent || customers["a"] == 0 || customers["b"] == 0 {
		t.Fatalf("%d keys for %d chains, customers %v: want five requests each, both customers in turn", len(keys), s.Sent, customers)
	}
}

func TestARefusedConfirmationEndsTheChainAsAnError(t *testing.T) {
	url, _ := standIn(t)
	w := World{Sales: url, Billing: url, Customers: []string{"refused"}, Goods: []Item{{"p", "C62", "wh"}}}
	p := Plan{Rate: 20, Duration: 500 * time.Millisecond, Chain: &Chain{Flow: "bill", World: w, RunID: "t"}}
	s := summarize(p, Run(context.Background(), client(), p))
	if s.OK != 0 || s.Errors["confirm: rejected over_limit"] != s.Sent {
		t.Fatalf("ok %d, errors %v; want every chain refused at confirm", s.OK, s.Errors)
	}
	if _, ok := s.Steps["deliver"]; ok {
		t.Fatal("a refused chain went on to deliver")
	}
}
