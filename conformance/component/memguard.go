package compconf

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultMaxHeapMB is the ceiling on the suite's own heap. The suite keeps every bus message,
// HTTP exchange, log line and span of a run in memory, so a component that floods one of them
// makes the suite grow without bound. On 2026-10-03 two suite processes reached 5 and 7.8 GiB
// on a 14 GiB host and the kernel started killing processes. Past the ceiling the run stops,
// says what grew, leaves a heap profile and removes its containers.
const DefaultMaxHeapMB = 1024

var (
	memGuardEvery      = 500 * time.Millisecond
	memGuardForceAfter = 30 * time.Second
)

type memGuard struct {
	tripped atomic.Bool
	heap    atomic.Uint64 // heap in use when the guard tripped
	limit   uint64
}

// maxHeapBytes is Options.MaxHeapMB, else COMPCONF_MAX_HEAP_MB, else the default; a negative
// value turns the guard off.
func maxHeapBytes(o Options) uint64 {
	mb := o.MaxHeapMB
	if mb == 0 {
		if v, err := strconv.Atoi(os.Getenv("COMPCONF_MAX_HEAP_MB")); err == nil {
			mb = v
		}
	}
	if mb == 0 {
		mb = DefaultMaxHeapMB
	}
	if mb < 0 {
		return 0
	}
	return uint64(mb) << 20
}

// startMemGuard watches the heap until stop is called. Past limit it records the trip, reports
// what grew, writes a heap profile and cancels the run. A step that ignores its context would
// keep the process alive, so after memGuardForceAfter the guard tears the run down itself and
// exits with status 3.
func (r *Run) startMemGuard(cancel context.CancelFunc, limit uint64) (stop func()) {
	if limit == 0 {
		return func() {}
	}
	r.mem.limit = limit
	done := make(chan struct{})
	var once sync.Once
	go func() {
		t := time.NewTicker(memGuardEvery)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
			}
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			if m.HeapAlloc <= limit {
				continue
			}
			r.mem.heap.Store(m.HeapAlloc)
			r.mem.tripped.Store(true)
			r.logf("memory guard: the suite's heap is %d MiB, over the ceiling of %d MiB; stopping the run\n%s",
				m.HeapAlloc>>20, limit>>20, r.memoryReport())
			r.writeHeapProfile()
			cancel()
			select {
			case <-done:
			case <-time.After(memGuardForceAfter):
				r.logf("memory guard: the run did not stop within %s; removing its containers and exiting", memGuardForceAfter)
				r.teardown()
				os.Exit(3)
			}
			return
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}

// memoryReport says how much each recorder holds; the heap profile has the rest.
func (r *Run) memoryReport() string {
	var b strings.Builder
	r.bus.mu.Lock()
	n, size := len(r.bus.msgs), 0
	per := map[string]int{}
	for _, m := range r.bus.msgs {
		size += len(m.Data)
		per[m.Subject]++
	}
	r.bus.mu.Unlock()
	fmt.Fprintf(&b, "  bus: %d messages, %d bytes", n, size)
	subjects := sortedKeys(per)
	sort.SliceStable(subjects, func(i, j int) bool { return per[subjects[i]] > per[subjects[j]] })
	for i, s := range subjects {
		if i == 5 {
			break
		}
		fmt.Fprintf(&b, "\n    %s ×%d", s, per[s])
	}
	if r.rec != nil {
		r.rec.mu.Lock()
		hn, hb := len(r.rec.all), 0
		for _, x := range r.rec.all {
			hb += len(x.Body)
		}
		r.rec.mu.Unlock()
		fmt.Fprintf(&b, "\n  http: %d exchanges, %d body bytes", hn, hb)
	}
	return b.String()
}

func (r *Run) writeHeapProfile() {
	dir := r.opt.OutDir
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.logf("memory guard: no heap profile: %v", err)
		return
	}
	f, err := os.Create(filepath.Join(dir, "compconf-heap.pprof"))
	if err != nil {
		r.logf("memory guard: no heap profile: %v", err)
		return
	}
	defer f.Close()
	if err := pprof.Lookup("heap").WriteTo(f, 0); err != nil {
		r.logf("memory guard: writing the heap profile: %v", err)
		return
	}
	r.logf("memory guard: heap profile at %s (go tool pprof -top <file>)", f.Name())
}
