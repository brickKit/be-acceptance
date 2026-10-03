package compconf

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// A tripped guard must say what grew, so the reader knows where to look.
func TestMemoryReportNamesWhatGrew(t *testing.T) {
	r := &Run{rec: &recorder{}}
	for i := 0; i < 3; i++ {
		r.bus.add(&nats.Msg{Subject: "erp.sales.order.confirmed.v1", Data: make([]byte, 1000)})
	}
	r.bus.add(&nats.Msg{Subject: "erp.sales.order.cancelled.v1", Data: make([]byte, 10)})
	r.rec.add(&exchange{Body: make([]byte, 500)})
	got := r.memoryReport()
	for _, want := range []string{
		"bus: 4 messages, 3010 bytes",
		"erp.sales.order.confirmed.v1 ×3",
		"http: 1 exchanges, 500 body bytes",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the report lacks %q:\n%s", want, got)
		}
	}
}

// A JetStream delivery to a consumer reaches a ">" subscription under the message's original
// subject. It is a delivery, not a publish: the recorder counts it per consumer and does not
// keep it, or a consumer that redelivers in a loop fills the suite's memory with "publishes".
func TestBusRecorderCountsDeliveriesInsteadOfKeepingThem(t *testing.T) {
	r := &Run{rec: &recorder{}}
	r.bus.add(&nats.Msg{Subject: "erp.sales.order.confirmed.v1", Data: []byte("{}")})
	for i := 0; i < 3; i++ {
		r.bus.add(&nats.Msg{Subject: "erp.sales.order.confirmed.v1", Data: []byte("{}"),
			Reply: "$JS.ACK.erp.sales__order.1.7.7.1700000000000000000.0"})
	}
	// With a domain, the ack subject carries the domain and an account hash before the stream.
	r.bus.add(&nats.Msg{Subject: "erp.sales.order.confirmed.v1", Data: []byte("{}"),
		Reply: "$JS.ACK.hub.ACCHASH.erp.sales__order.1.8.8.1700000000000000000.0.tok"})
	if n := len(r.bus.msgs); n != 1 {
		t.Fatalf("kept %d messages, want only the 1 publish", n)
	}
	if got := r.bus.deliveries["erp/sales__order"]; got != 4 {
		t.Errorf("deliveries of erp/sales__order = %d, want 4", got)
	}
	if rep := r.memoryReport(); !strings.Contains(rep, "erp/sales__order ×4") {
		t.Errorf("the report lacks the delivery count:\n%s", rep)
	}
}

// Past the ceiling the guard cancels the run, records that it tripped and leaves a heap
// profile; it does not kill the process while the run unwinds in time.
func TestMemGuardCancelsTheRunPastTheCeiling(t *testing.T) {
	r := &Run{rec: &recorder{}, opt: Options{OutDir: t.TempDir(), Progress: io.Discard}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := r.startMemGuard(cancel, 1) // one byte: any live heap is over it
	defer stop()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the guard did not cancel the run")
	}
	if !r.mem.tripped.Load() {
		t.Error("the guard cancelled the run without recording that it tripped")
	}
	if _, err := os.Stat(filepath.Join(r.opt.OutDir, "compconf-heap.pprof")); err != nil {
		t.Errorf("no heap profile next to the report: %v", err)
	}
}

func TestMemGuardStaysQuietUnderTheCeiling(t *testing.T) {
	r := &Run{rec: &recorder{}, opt: Options{OutDir: t.TempDir(), Progress: io.Discard}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := r.startMemGuard(cancel, 1<<40)
	time.Sleep(3 * memGuardEvery)
	stop()
	if ctx.Err() != nil || r.mem.tripped.Load() {
		t.Error("the guard tripped under the ceiling")
	}
}
