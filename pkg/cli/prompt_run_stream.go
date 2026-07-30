package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/session"
)

// runSubBuffer bounds each SSE subscriber's channel. A subscriber that can't
// keep up is dropped (and reconnects to replay) rather than stalling the run.
const runSubBuffer = 64

// runStream is the in-process pub/sub buffer for one prompt run's session.Message
// frames. Every frame is buffered for replay to late/reconnecting subscribers
// and fanned out to current subscribers.
type runStream struct {
	mu            sync.Mutex
	entries       []session.Message
	subs          map[chan session.Message]struct{}
	eventSubs     map[chan runStreamEvent]struct{}
	run           PromptRunFrame
	chatState     *ChatStateFrame
	cancel        context.CancelFunc
	stopRequested bool
	done          bool
	summary       *PromptRunSummary
	errMsg        string
	endedAt       time.Time
}

func newRunStream() *runStream {
	return &runStream{
		subs:      map[chan session.Message]struct{}{},
		eventSubs: map[chan runStreamEvent]struct{}{},
	}
}

type runStreamEvent struct {
	name string
	data any
}

// publish appends a frame and fans it out. A subscriber whose buffer is full is
// dropped (it can reconnect and replay) rather than blocking the producer.
func (s *runStream) publish(e session.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return
	}
	s.entries = append(s.entries, e)
	for ch := range s.subs {
		select {
		case ch <- e:
		default:
			delete(s.subs, ch)
			close(ch)
		}
	}
	s.publishEventLocked(runStreamEvent{name: "entry", data: e})
}

func (s *runStream) setRun(frame PromptRunFrame) {
	s.mu.Lock()
	s.run = frame
	s.publishEventLocked(runStreamEvent{name: "run", data: frame})
	s.mu.Unlock()
}

func (s *runStream) setChatState(frame ChatStateFrame) {
	s.mu.Lock()
	copy := frame
	copy.Queued = append([]ChatQueuedMessage(nil), frame.Queued...)
	copy.DiscardedMessageIDs = append([]string(nil), frame.DiscardedMessageIDs...)
	s.chatState = &copy
	s.run.SessionID = frame.SessionID
	s.run.Capabilities = frame.Capabilities
	s.publishEventLocked(runStreamEvent{name: "state", data: copy})
	s.mu.Unlock()
}

func (s *runStream) setCancel(cancel context.CancelFunc) {
	s.mu.Lock()
	s.cancel = cancel
	requested := s.stopRequested
	s.mu.Unlock()
	if requested {
		cancel()
	}
}

func (s *runStream) requestStop() bool {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return false
	}
	s.stopRequested = true
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return true
}

func (s *runStream) wasStopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopRequested
}

func (s *runStream) publishEventLocked(event runStreamEvent) {
	for ch := range s.eventSubs {
		select {
		case ch <- event:
		default:
			delete(s.eventSubs, ch)
			close(ch)
		}
	}
}

// complete marks the run finished successfully and closes all subscribers.
func (s *runStream) complete(sum PromptRunSummary) { s.finish(&sum, "") }

// fail marks the run finished with an error and closes all subscribers.
func (s *runStream) fail(msg string) PromptRunSummary {
	summary := PromptRunSummary{Error: msg}
	s.finish(&summary, msg)
	return summary
}

func (s *runStream) finish(sum *PromptRunSummary, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return
	}
	s.done = true
	s.errMsg = errMsg
	s.endedAt = time.Now()
	if errMsg != "" {
		s.run.Status = "error"
	} else {
		s.run.Status = "done"
	}
	if sum != nil {
		if sum.RunID == "" {
			sum.RunID = s.run.RunID
		}
		if sum.SessionID == "" {
			sum.SessionID = s.run.SessionID
		}
		if sum.Model == "" {
			sum.Model = s.run.Model
		}
		if sum.Backend == "" {
			sum.Backend = s.run.Backend
		}
		if sum.SessionID != "" {
			s.run.SessionID = sum.SessionID
		}
	}
	s.summary = sum
	for ch := range s.subs {
		delete(s.subs, ch)
		close(ch)
	}
	for ch := range s.eventSubs {
		delete(s.eventSubs, ch)
		close(ch)
	}
}

func (s *runStream) setRunMetadata(sessionID, model string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sessionID != "" {
		s.run.SessionID = sessionID
	}
	if model != "" {
		s.run.Model = model
	}
}

// subscribe atomically snapshots the replay buffer and registers a live channel
// under one lock, so no frame is missed between replay and live delivery. When
// the run already finished, ch is nil and the terminal state is returned.
func (s *runStream) subscribe() (replay []session.Message, ch chan session.Message, done bool, summary *PromptRunSummary, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	replay = append([]session.Message(nil), s.entries...)
	if s.done {
		return replay, nil, true, s.summary, s.errMsg
	}
	ch = make(chan session.Message, runSubBuffer)
	s.subs[ch] = struct{}{}
	return replay, ch, false, nil, ""
}

func (s *runStream) subscribeEvents() (PromptRunFrame, []session.Message, *ChatStateFrame, chan runStreamEvent, bool, *PromptRunSummary, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	replay := append([]session.Message(nil), s.entries...)
	state := cloneChatState(s.chatState)
	if s.done {
		return s.run, replay, state, nil, true, s.summary, s.errMsg
	}
	ch := make(chan runStreamEvent, runSubBuffer)
	s.eventSubs[ch] = struct{}{}
	return s.run, replay, state, ch, false, nil, ""
}

func (s *runStream) unsubscribeEvents(ch chan runStreamEvent) {
	if ch == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.eventSubs[ch]; ok {
		delete(s.eventSubs, ch)
		close(ch)
	}
}

func (s *runStream) state() (bool, *PromptRunSummary, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done, s.summary, s.errMsg
}

func (s *runStream) snapshot() ([]session.Message, bool, *PromptRunSummary, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]session.Message(nil), s.entries...), s.done, s.summary, s.errMsg
}

func (s *runStream) terminalAt() (bool, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done, s.endedAt
}

// runBroker owns the live runStreams keyed by run id.
type runBroker struct {
	mu   sync.Mutex
	runs map[string]*runStream
}

var promptRuns = &runBroker{runs: map[string]*runStream{}}

func (b *runBroker) create(runID string) *runStream {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := newRunStream()
	b.runs[runID] = s
	return s
}

func (b *runBroker) get(runID string) (*runStream, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.runs[runID]
	return s, ok
}

// prunePromptRuns periodically evicts finished runs older than the retention
// window (aligned with clicky's ~10min task GC) until ctx is cancelled.
func prunePromptRuns(ctx context.Context, b *runBroker) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.prune(15 * time.Minute)
		}
	}
}

// prune drops finished runs whose end time is older than maxAge. Live runs are
// always retained.
func (b *runBroker) prune(maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, s := range b.runs {
		if done, end := s.terminalAt(); done && !end.IsZero() && end.Before(cutoff) {
			delete(b.runs, id)
		}
	}
}

type promptRunSnapshotBody struct {
	Run     PromptRunFrame    `json:"run"`
	Entries []session.Message `json:"entries"`
	State   *ChatStateFrame   `json:"state,omitempty"`
	Done    bool              `json:"done"`
	Summary *PromptRunSummary `json:"summary,omitempty"`
	Error   string            `json:"error,omitempty"`
}

// handlePromptRunStream streams a run's session.Message frames as SSE:
//
//	event: entry  data: <session.Message>   (one per frame; replayed on connect)
//	event: done   data: <PromptRunSummary>   (terminal, success)
//	event: error  data: <PromptRunSummary>   (terminal, failure)
func handlePromptRunStream(b *runBroker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stream, ok := b.get(r.PathValue("runId"))
		if !ok {
			http.Error(w, "unknown run", http.StatusNotFound)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		setSSEHeaders(w)

		run, replay, state, ch, done, summary, errMsg := stream.subscribeEvents()
		defer stream.unsubscribeEvents(ch)
		writeSSE(w, "run", run)
		for _, e := range replay {
			writeSSE(w, "entry", e)
		}
		if state != nil {
			writeSSE(w, "state", state)
		}
		if done {
			writeTerminal(w, summary, errMsg)
			flusher.Flush()
			return
		}
		flusher.Flush()

		heartbeat := time.NewTicker(15 * time.Second)
		defer heartbeat.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case event, open := <-ch:
				if !open {
					if d, sum, em := stream.state(); d {
						writeTerminal(w, sum, em)
						flusher.Flush()
					}
					return
				}
				writeSSE(w, event.name, event.data)
				flusher.Flush()
			case <-heartbeat.C:
				fmt.Fprint(w, ": ping\n\n")
				flusher.Flush()
			}
		}
	}
}

// handlePromptRunSnapshot returns the full buffered state for initial load or
// reconnect fallback.
func handlePromptRunSnapshot(b *runBroker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stream, ok := b.get(r.PathValue("runId"))
		if !ok {
			http.Error(w, "unknown run", http.StatusNotFound)
			return
		}
		run, entries, state, ch, done, summary, errMsg := stream.subscribeEvents()
		stream.unsubscribeEvents(ch)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(promptRunSnapshotBody{Run: run, Entries: entries, State: state, Done: done, Summary: summary, Error: errMsg})
	}
}

func cloneChatState(state *ChatStateFrame) *ChatStateFrame {
	if state == nil {
		return nil
	}
	copy := *state
	copy.Queued = append([]ChatQueuedMessage(nil), state.Queued...)
	copy.DiscardedMessageIDs = append([]string(nil), state.DiscardedMessageIDs...)
	return &copy
}

func setSSEHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
}

func writeSSE(w http.ResponseWriter, event string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		log.Warnf("prompt run stream: dropping %q frame: marshal failed: %v", event, err)
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
}

func writeTerminal(w http.ResponseWriter, summary *PromptRunSummary, errMsg string) {
	switch {
	case errMsg != "":
		if summary != nil {
			writeSSE(w, "error", summary)
		} else {
			writeSSE(w, "error", map[string]string{"error": errMsg})
		}
	case summary != nil:
		writeSSE(w, "done", summary)
	default:
		writeSSE(w, "done", map[string]string{"status": "completed"})
	}
}
