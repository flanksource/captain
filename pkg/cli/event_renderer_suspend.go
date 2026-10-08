package cli

import (
	"errors"
	"fmt"
	"sync"

	"github.com/charmbracelet/x/ansi"
	"github.com/flanksource/captain/pkg/ai"
)

// heldEvent is an event that arrived while the renderer was suspended.
type heldEvent struct {
	iteration int
	event     ai.Event
}

// Handle renders one event, or holds it while the renderer is suspended.
func (r *EventRenderer) Handle(iteration int, event ai.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.suspended > 0 {
		r.held = append(r.held, heldEvent{iteration: iteration, event: event})
		return
	}
	r.handle(iteration, event)
}

// Flush writes whatever turn is still pending. Flushing while suspended is an
// error: the events held for the prompt would be written over it or lost.
func (r *EventRenderer) Flush() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.suspended > 0 {
		return errors.Join(r.err, fmt.Errorf("event renderer flushed while suspended, holding %d events", len(r.held)))
	}
	return r.flush()
}

// Suspend stops the renderer drawing so something else — an approval form —
// can own the terminal. The in-place line is erased, and events that arrive
// meanwhile are held rather than written. The returned resume redraws the
// in-place turn and writes the held events in order; calling it again is a
// no-op. Suspensions nest: output resumes once every one of them has resumed.
// Both Suspend and resume may be called from any goroutine.
func (r *EventRenderer) Suspend() (resume func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.suspended++
	if r.suspended == 1 && (r.progressDrawn || r.pendingDrawn) {
		// The progress line is superseded state and is not redrawn. The pending
		// turn is marked dirty, so whatever finishes it rewrites the erased line
		// instead of only ending it.
		r.progressDrawn = false
		r.pendingDirty = r.pendingDrawn
		r.writeRaw("\r" + ansi.EraseEntireLine)
	}
	var once sync.Once
	return func() { once.Do(r.resume) }
}

func (r *EventRenderer) resume() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.suspended--
	if r.suspended > 0 {
		return
	}
	held := r.held
	r.held = nil
	for _, item := range held {
		r.handle(item.iteration, item.event)
	}
	// Replay redraws are throttled, so the erased turn may still be blank.
	if r.pendingDrawn && r.pendingDirty && r.pending != nil {
		r.writePending()
	}
}
