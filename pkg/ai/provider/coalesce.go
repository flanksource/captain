package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
)

// CoalesceStream drains an event channel and returns the equivalent buffered
// ai.Response. Loop drivers that want a per-iteration "final answer" alongside
// the live event stream call this on a tee'd channel; for one-shot use, prefer
// Provider.Execute.
func CoalesceStream(ctx context.Context, model string, events <-chan ai.Event, start time.Time) (*ai.Response, error) {
	var (
		text       strings.Builder
		usage      ai.Usage
		lastResult *ai.Event
		errEvents  []ai.Event
		sessionID  string
	)

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return finaliseCoalescedResponse(model, text.String(), usage, lastResult, errEvents, sessionID, start)
			}
			switch ev.Kind {
			case ai.EventText:
				text.WriteString(ev.Text)
			case ai.EventResult:
				cp := ev
				lastResult = &cp
				if ev.Usage != nil {
					usage = *ev.Usage
				}
			case ai.EventSystem:
				if ev.SessionID != "" {
					sessionID = ev.SessionID
				}
			case ai.EventError:
				errEvents = append(errEvents, ev)
			}
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: context cancelled", ai.ErrTimeout)
		}
	}
}

func finaliseCoalescedResponse(model, text string, usage ai.Usage, lastResult *ai.Event, errEvents []ai.Event, sessionID string, start time.Time) (*ai.Response, error) {
	if lastResult != nil && !lastResult.Success {
		msg := lastResult.Error
		if msg == "" {
			msg = "claude returned is_error=true"
		}
		return nil, fmt.Errorf("%w: %s", ai.ErrCLIExecutionFailed, msg)
	}
	if lastResult == nil && len(errEvents) > 0 {
		return nil, fmt.Errorf("%w: %s", ai.ErrCLIExecutionFailed, errEvents[len(errEvents)-1].Error)
	}

	resp := &ai.Response{
		Text:     text,
		Model:    model,
		Backend:  ai.BackendClaudeCLI,
		Usage:    usage,
		Duration: time.Since(start),
	}
	if lastResult != nil {
		resp.Raw = lastResult.Input
		// Carry any validated structured output (raw JSON) the terminal result
		// event holds; the caller's Execute binds it into its schema target.
		if len(lastResult.StructuredData) > 0 {
			resp.StructuredData = lastResult.StructuredData
		}
	}
	if sessionID != "" {
		if resp.Raw == nil {
			resp.Raw = map[string]any{"session_id": sessionID}
		}
	}
	return resp, nil
}
