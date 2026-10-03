package compconf

import (
	"fmt"
	"sync"
	"time"
)

// evidence accumulates, per case ID, what the scenario observed. A case may be exercised by
// several steps (CP-CORE-05 is observed before, at and after readiness); it passes when it was
// exercised and nothing failed.
type evidence struct {
	cat  *Catalog
	mu   sync.Mutex
	outs map[string]*outcome
}

type outcome struct {
	touched bool
	fails   []string
	notes   []string
	na      string
	elapsed time.Duration
}

func newEvidence(cat *Catalog) *evidence { return &evidence{cat: cat, outs: map[string]*outcome{}} }

func (e *evidence) get(id string) *outcome {
	if _, ok := e.cat.Case(id); !ok {
		panic("compconf: unknown case " + id)
	}
	o := e.outs[id]
	if o == nil {
		o = &outcome{}
		e.outs[id] = o
	}
	return o
}

// pass marks a case exercised.
func (e *evidence) pass(id string, note ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	o := e.get(id)
	o.touched = true
	o.notes = append(o.notes, note...)
}

// fail records a failure.
func (e *evidence) fail(id, format string, args ...any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	o := e.get(id)
	o.touched = true
	o.fails = append(o.fails, fmt.Sprintf(format, args...))
}

// check records a failure when ok is false, and returns ok.
func (e *evidence) check(id string, ok bool, format string, args ...any) bool {
	if ok {
		e.pass(id)
	} else {
		e.fail(id, format, args...)
	}
	return ok
}

// notApplicable marks a case that cannot apply to this component, with the reason.
func (e *evidence) notApplicable(id, reason string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.get(id).na = reason
}

func (e *evidence) addTime(id string, d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.get(id).elapsed += d
}

func (e *evidence) outcome(id string) outcome {
	e.mu.Lock()
	defer e.mu.Unlock()
	if o, ok := e.outs[id]; ok {
		return *o
	}
	return outcome{}
}
