package aichat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/flanksource/captain/pkg/api"
)

var errNoActiveTurn = errors.New("chat session has no active turn")
var errThreadBusy = errors.New("chat session is busy")

type activeTurn struct {
	provider  api.StreamingProvider
	execution Execution
	ctx       context.Context
	cancel    context.CancelFunc

	mu           sync.Mutex
	interrupting bool
	interrupted  bool
	done         bool
	signal       chan struct{}
	aborted      chan struct{}
	emitted      chan struct{}
}

func newActiveTurn(ctx context.Context, provider api.StreamingProvider, execution Execution, cancel context.CancelFunc) *activeTurn {
	return &activeTurn{
		provider: provider, execution: execution, ctx: ctx, cancel: cancel,
		signal: make(chan struct{}), aborted: make(chan struct{}), emitted: make(chan struct{}),
	}
}

func (t *activeTurn) stream(source <-chan api.Event) <-chan api.Event {
	out := make(chan api.Event)
	go func() {
		defer close(out)
		emitInterrupted := func() {
			select {
			case out <- api.Event{Kind: api.EventInterrupted, Reason: "user"}:
			case <-t.ctx.Done():
			}
			close(t.emitted)
		}
		for {
			select {
			case <-t.signal:
				emitInterrupted()
				return
			case event, ok := <-source:
				if !ok {
					t.mu.Lock()
					interrupting := t.interrupting
					if !interrupting {
						t.done = true
					}
					t.mu.Unlock()
					if interrupting {
						select {
						case <-t.signal:
							emitInterrupted()
						case <-t.aborted:
						case <-t.ctx.Done():
						}
					}
					return
				}
				t.mu.Lock()
				interrupted := t.interrupted
				t.mu.Unlock()
				if !interrupted {
					select {
					case out <- event:
					case <-t.ctx.Done():
						return
					}
				}
			case <-t.ctx.Done():
				return
			}
		}
	}()
	return out
}

func (t *activeTurn) interrupt(ctx context.Context) error {
	t.mu.Lock()
	if t.done || t.interrupting || t.interrupted {
		t.mu.Unlock()
		return errNoActiveTurn
	}
	t.interrupting = true
	t.mu.Unlock()

	if provider, ok := api.ProviderAs[api.InterruptibleProvider](t.provider); ok {
		if err := provider.Interrupt(ctx); err != nil {
			t.abortInterrupt()
			return fmt.Errorf("interrupt provider turn: %w", err)
		}
	}
	if t.execution != nil {
		if err := t.execution.Interrupt(ctx, "user"); err != nil {
			t.abortInterrupt()
			return fmt.Errorf("interrupt authoritative execution: %w", err)
		}
	}

	t.mu.Lock()
	t.interrupted = true
	close(t.signal)
	t.mu.Unlock()
	select {
	case <-t.emitted:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *activeTurn) abortInterrupt() {
	t.mu.Lock()
	t.interrupting = false
	close(t.aborted)
	t.mu.Unlock()
}

func (t *activeTurn) finish() {
	t.cancel()
	t.mu.Lock()
	t.done = true
	t.mu.Unlock()
}

func (s *Service) registerActiveTurn(threadID string, turn *activeTurn) error {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	if active, exists := s.active[threadID]; exists {
		if sameExecutionTurn(active.execution, turn.execution) {
			delete(s.pending, threadID)
			s.active[threadID] = turn
			return nil
		}
		return fmt.Errorf("%w: chat session %s already has an active turn", errThreadBusy, threadID)
	}
	if _, reserved := s.pending[threadID]; !reserved {
		return fmt.Errorf("%w: chat session %s has no reserved turn to activate", errThreadBusy, threadID)
	}
	delete(s.pending, threadID)
	s.active[threadID] = turn
	return nil
}

func sameExecutionTurn(left, right Execution) bool {
	return left != nil && right != nil && left.TurnID() != "" && left.TurnID() == right.TurnID()
}

func (s *Service) reserveThread(threadID string) error {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	if _, exists := s.active[threadID]; exists {
		return fmt.Errorf("%w: chat session %s already has an active turn", errThreadBusy, threadID)
	}
	if _, exists := s.pending[threadID]; exists {
		return fmt.Errorf("%w: chat session %s already has a turn being admitted", errThreadBusy, threadID)
	}
	s.pending[threadID] = struct{}{}
	return nil
}

// reserveThreadForApproval claims the admission slot before a durable approval
// is consumed. If the suspending turn is still active, the reservation keeps
// the slot claimed while ownership is handed to its continuation.
func (s *Service) reserveThreadForApproval(threadID string) (string, error) {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	if _, exists := s.pending[threadID]; exists {
		return "", fmt.Errorf("%w: chat session %s already has a turn being admitted", errThreadBusy, threadID)
	}
	if active, exists := s.active[threadID]; exists {
		turnID := ""
		if active.execution != nil {
			turnID = active.execution.TurnID()
		}
		s.pending[threadID] = struct{}{}
		return turnID, nil
	}
	s.pending[threadID] = struct{}{}
	return "", nil
}

func (s *Service) releaseThreadReservation(threadID string) {
	s.activeMu.Lock()
	delete(s.pending, threadID)
	s.activeMu.Unlock()
}

func (s *Service) unregisterActiveTurn(threadID string, turn *activeTurn) {
	turn.finish()
	s.activeMu.Lock()
	if s.active[threadID] == turn {
		delete(s.active, threadID)
	}
	s.activeMu.Unlock()
}

func (s *Service) handleInterrupt(w http.ResponseWriter, request *http.Request) {
	store := s.threadStore(w, request)
	if store == nil {
		return
	}
	threadID := request.PathValue("id")
	if _, err := store.Get(request.Context(), threadID); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	s.activeMu.Lock()
	turn := s.active[threadID]
	s.activeMu.Unlock()
	if turn == nil {
		http.Error(w, errNoActiveTurn.Error(), http.StatusConflict)
		return
	}
	if err := turn.interrupt(request.Context()); err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, errNoActiveTurn) {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	if sessions, ok := store.(SessionReader); ok {
		aggregate, err := sessions.GetSession(request.Context(), threadID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = writeJSON(w, http.StatusOK, aggregate)
		return
	}
	thread, err := store.Get(request.Context(), threadID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = writeJSON(w, http.StatusOK, thread)
}
