package query

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
)

// FollowOptions controls a live session follow.
type FollowOptions struct {
	// Replay emits every message the session already holds before following.
	// Without it only messages that appear or change afterwards are emitted.
	Replay bool
}

// FollowState is the session's lifecycle position plus a fingerprint of the
// non-message facets the aggregate composes from the store. A consumer
// refetches the aggregate (plan, approvals, todos, files, costs) when Revision
// advances or Facets changes; Facets is opaque and only compared for equality.
type FollowState struct {
	Revision        int64  `json:"revision"`
	LifecycleStatus string `json:"lifecycleStatus"`
	ActivityState   string `json:"activityState"`
	Facets          string `json:"facets"`
}

// FollowEvent is exactly one of: a message upsert keyed by Message.ID, a state
// change, or a terminal error after which the stream closes.
type FollowEvent struct {
	Message *session.Message
	State   *FollowState
	Err     error
}

var terminalLifecycles = map[string]bool{
	string(database.SessionLifecycleSucceeded): true,
	"partial":                                    true,
	string(database.SessionLifecycleFailed):      true,
	string(database.SessionLifecycleCancelled):   true,
	string(database.SessionLifecycleInterrupted): true,
}

// Follow streams a session's unified messages and state as they change,
// driven by the captain_session_change notifications migration 83 emits.
//
// The identity resolves exactly as a session read does (ResolveFolded), and
// the whole thread is followed: the session, the transcript row it executed in
// and every session whose messages the thread lists. Each wake-up re-reads the
// thread and emits only messages whose content changed, so an enriched message
// is emitted again under the same id. Before the transcript reaches the
// database, messages come from the provider's log file instead.
//
// The channel closes when the session reaches a terminal lifecycle, when ctx
// is done, or after an error event. A LISTEN that cannot be established fails
// the call; there is no polling fallback.
func Follow(ctx context.Context, db *database.DB, id string, opts FollowOptions) (<-chan FollowEvent, error) {
	identity := strings.TrimSpace(id)
	if identity == "" {
		return nil, fmt.Errorf("%w: a session identity is required to follow", database.ErrInvalidSession)
	}
	hub, err := hubFor(db)
	if err != nil {
		return nil, err
	}
	f := &follower{
		db: db, identity: identity, replay: opts.Replay,
		seen: map[string]string{}, owners: map[string]database.SessionOverview{},
	}
	thread, err := f.resolve(ctx)
	if err != nil {
		return nil, err
	}
	sub, err := hub.subscribe(ctx, thread.sessionIDs(nil))
	if err != nil {
		return nil, err
	}
	events := make(chan FollowEvent)
	go f.run(ctx, sub, events)
	return events, nil
}

type follower struct {
	db       *database.DB
	identity string
	replay   bool
	primed   bool
	seen     map[string]string
	owners   map[string]database.SessionOverview
	state    *FollowState
	tail     *transcriptTail
}

func (f *follower) run(ctx context.Context, sub *sessionSubscription, events chan<- FollowEvent) {
	defer close(events)
	defer f.stopTail()
	defer sub.close()
	for {
		pending, terminal, err := f.cycle(ctx, sub)
		if err != nil {
			pending = append(pending, FollowEvent{Err: err})
		}
		for _, event := range pending {
			select {
			case events <- event:
			case <-ctx.Done():
				return
			}
		}
		if err != nil || terminal {
			return
		}
		tailSignal, tailErrs := f.tailChannels()
		select {
		case <-ctx.Done():
			return
		case <-sub.signal:
		case <-tailSignal:
		case err := <-sub.failed:
			f.emitFinal(ctx, events, err)
			return
		case err := <-tailErrs:
			f.emitFinal(ctx, events, err)
			return
		}
	}
}

func (f *follower) emitFinal(ctx context.Context, events chan<- FollowEvent, err error) {
	select {
	case events <- FollowEvent{Err: fmt.Errorf("follow Captain session %s: %w", f.identity, err)}:
	case <-ctx.Done():
	}
}

// cycle re-reads the thread once and returns the events it produced, and
// whether the session has reached a terminal lifecycle.
func (f *follower) cycle(ctx context.Context, sub *sessionSubscription) ([]FollowEvent, bool, error) {
	thread, err := f.resolve(ctx)
	if err != nil {
		return nil, false, err
	}
	rows, err := f.db.ListThreadTranscriptMessages(ctx, thread.source.ID)
	if err != nil {
		return nil, false, fmt.Errorf("follow Captain session %s: %w", f.identity, err)
	}
	sub.follow(thread.sessionIDs(rows))
	messages, err := f.threadMessages(ctx, thread, rows)
	if err != nil {
		return nil, false, err
	}
	pending, err := f.changedMessages(messages)
	if err != nil {
		return nil, false, err
	}
	facets, err := f.facets(ctx, thread)
	if err != nil {
		return nil, false, err
	}
	overview := thread.primary.Session
	state := FollowState{
		Revision: overview.StateVersion, LifecycleStatus: overview.LifecycleStatus, ActivityState: overview.ActivityState,
		Facets: facets,
	}
	if f.state == nil || *f.state != state {
		f.state = &state
		pending = append(pending, FollowEvent{State: &state})
	}
	f.primed = true
	return pending, terminalLifecycles[state.LifecycleStatus], nil
}

// changedMessages keeps the messages whose content differs from what was last
// emitted under the same id. The first read only primes the fingerprints when
// Replay is off.
func (f *follower) changedMessages(messages []session.Message) ([]FollowEvent, error) {
	var pending []FollowEvent
	for i := range messages {
		fingerprint, err := json.Marshal(messages[i])
		if err != nil {
			return nil, fmt.Errorf("fingerprint Captain message %s: %w", messages[i].ID, err)
		}
		if f.seen[messages[i].ID] == string(fingerprint) {
			continue
		}
		f.seen[messages[i].ID] = string(fingerprint)
		if f.primed || f.replay {
			pending = append(pending, FollowEvent{Message: &messages[i]})
		}
	}
	return pending, nil
}
