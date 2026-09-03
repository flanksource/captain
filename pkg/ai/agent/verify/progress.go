package verify

import (
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/api"
)

// ProgressInterval bounds how often an in-flight snapshot reaches a reader. A
// fixture runner reports per test; a reader needs to see that something is
// moving, not every row as it lands, and every snapshot costs an event on the
// run's stream and a redraw in whatever is watching it.
const ProgressInterval = 500 * time.Millisecond

// ProgressVerifier is a Verifier that can report where it has got to before it
// has a verdict. The Plugin hands it a sink; a verifier that implements nothing
// here is simply silent until it finishes.
type ProgressVerifier interface {
	SetProgress(func(api.VerifyReport))
}

// progressEmitter rate-limits in-flight snapshots to one per interval and
// guarantees the last one is delivered: a check that reports ten rows in a
// hundred milliseconds and then blocks for a minute must still leave the reader
// looking at the tenth row, not the first.
type progressEmitter struct {
	mu       sync.Mutex
	interval time.Duration
	sinks    []func(api.VerifyReport)
	lastAt   time.Time
	pending  *api.VerifyReport
}

func newProgressEmitter(interval time.Duration, sinks ...func(api.VerifyReport)) *progressEmitter {
	live := make([]func(api.VerifyReport), 0, len(sinks))
	for _, sink := range sinks {
		if sink != nil {
			live = append(live, sink)
		}
	}
	return &progressEmitter{interval: interval, sinks: live}
}

// publish takes one snapshot from the verifier. It emits immediately when the
// window has elapsed, and otherwise holds the snapshot for flush.
func (e *progressEmitter) publish(report api.VerifyReport) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.sinks) == 0 {
		return
	}
	now := time.Now()
	if !e.lastAt.IsZero() && now.Sub(e.lastAt) < e.interval {
		e.pending = &report
		return
	}
	e.lastAt = now
	e.pending = nil
	e.deliver(report)
}

// flush delivers the snapshot the window held back. It runs before the verdict
// so the two arrive in the order they happened.
func (e *progressEmitter) flush() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pending == nil {
		return
	}
	report := *e.pending
	e.pending = nil
	e.lastAt = time.Now()
	e.deliver(report)
}

// deliver fans one snapshot out to every sink. Callers hold e.mu.
func (e *progressEmitter) deliver(report api.VerifyReport) {
	for _, sink := range e.sinks {
		sink(report)
	}
}
