package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi"
	"github.com/flanksource/captain/pkg/ai"
)

// heldEvent is an event, or out-of-band lines, that arrived while the renderer
// was suspended. Both are held in one queue so they replay in arrival order.
type heldEvent struct {
	iteration int
	event     ai.Event
	lines     string
}

// Writer returns the writer for output that is not an event — log lines, a
// child process's stderr. Whole lines are committed above the in-place turn,
// and while an approval form owns the terminal they are held with the events,
// so nothing is drawn over the form or through the turn being streamed.
func (r *EventRenderer) Writer() io.Writer { return outOfBandWriter{r} }

type outOfBandWriter struct{ r *EventRenderer }

func (w outOfBandWriter) Write(p []byte) (int, error) {
	r := w.r
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outOfBand.Write(p)
	buffered := r.outOfBand.String()
	end := strings.LastIndexByte(buffered, '\n')
	if end < 0 {
		return len(p), nil
	}
	r.outOfBand.Reset()
	r.outOfBand.WriteString(buffered[end+1:])
	if r.suspended > 0 {
		r.held = append(r.held, heldEvent{lines: buffered[:end+1]})
		return len(p), nil
	}
	r.writeOutOfBand(buffered[:end+1])
	return len(p), r.err
}

// writeOutOfBand commits lines without corrupting the in-place line: it is
// erased first, and the pending turn is redrawn beneath what was written.
func (r *EventRenderer) writeOutOfBand(lines string) {
	redraw := r.pendingDrawn && r.pending != nil
	if r.progressDrawn || r.pendingDrawn {
		r.progressDrawn = false
		r.writeRaw("\r" + ansi.EraseEntireLine)
	}
	r.write(lines)
	if redraw {
		r.writePending()
	}
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
	if r.outOfBand.Len() > 0 {
		partial := r.outOfBand.String()
		r.outOfBand.Reset()
		r.flushPending()
		r.writeOutOfBand(partial + "\n")
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
		if item.lines != "" {
			r.writeOutOfBand(item.lines)
			continue
		}
		r.handle(item.iteration, item.event)
	}
	// Replay redraws are throttled, so the erased turn may still be blank.
	if r.pendingDrawn && r.pendingDirty && r.pending != nil {
		r.writePending()
	}
}
