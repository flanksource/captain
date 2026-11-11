package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	sessionquery "github.com/flanksource/captain/pkg/session/query"
	"github.com/flanksource/clicky/task"
)

// captainSessionType is the SessionRef type a captain session is named under.
const captainSessionType = "captain"

// captainSessionSource follows captain sessions for clicky's task stream, so a
// task view receives a run's live session entries over the run's own
// /tasks/stream connection instead of opening a session follow of its own.
type captainSessionSource struct{ open sessionDB }

// Follow relays the session's follow without replay: the task stream opens it
// before the client sees the session ref, and the client reads the history it
// holds by then over HTTP, so only what the session gains afterwards is sent.
func (s captainSessionSource) Follow(ctx context.Context, ref task.SessionRef) (<-chan task.SessionEvent, error) {
	db, err := s.open(ctx)
	if err != nil {
		return nil, fmt.Errorf("open the database to follow captain session %s: %w", ref.ID, err)
	}
	follow, err := sessionquery.Follow(ctx, db, ref.ID, sessionquery.FollowOptions{})
	if err != nil {
		return nil, err
	}
	events := make(chan task.SessionEvent)
	go func() {
		defer close(events)
		for event := range follow {
			select {
			case events <- sessionTaskEvent(event):
			case <-ctx.Done():
				return
			}
		}
	}()
	return events, nil
}

// sessionTaskEvent is a follow event as the task stream relays it: the message
// or state as the JSON the session's own ?follow=1 stream serves, or its error.
func sessionTaskEvent(event sessionquery.FollowEvent) task.SessionEvent {
	var payload any
	switch {
	case event.Err != nil:
		return task.SessionEvent{Err: event.Err}
	case event.Message != nil:
		payload = event.Message
	case event.State != nil:
		payload = event.State
	default:
		return task.SessionEvent{Err: errors.New("captain session follow produced an empty event")}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return task.SessionEvent{Err: fmt.Errorf("encode captain session event: %w", err)}
	}
	if event.Message != nil {
		return task.SessionEvent{Entry: data}
	}
	return task.SessionEvent{State: data}
}
