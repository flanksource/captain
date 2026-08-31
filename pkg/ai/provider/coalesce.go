package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/observation"
)

// CoalesceStream drains an event channel and returns the equivalent buffered
// ai.Response. Loop drivers that want a per-iteration "final answer" alongside
// the live event stream call this on a tee'd channel; for one-shot use, prefer
// Provider.Execute.
func CoalesceStream(ctx context.Context, model string, events <-chan ai.Event, start time.Time) (*ai.Response, error) {
	return CoalesceStreamForBackend(ctx, ai.BackendClaudeCLI, model, events, start)
}

func CoalesceStreamForBackend(ctx context.Context, backend ai.Backend, model string, events <-chan ai.Event, start time.Time) (*ai.Response, error) {
	var (
		text       strings.Builder
		usage      ai.Usage
		lastResult *ai.Event
		errEvents  []ai.Event
		sessionID  string
		outcome    *ai.TerminalOutcome
		outcomeErr error
	)

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				var terminalUsage *ai.Usage
				if lastResult != nil {
					terminalUsage = lastResult.Usage
				}
				observation.RecordUsage(ctx, terminalUsage)
				if outcomeErr != nil {
					return nil, fmt.Errorf("invalid terminal outcome: %w", outcomeErr)
				}
				resp, err := finaliseCoalescedResponse(backend, model, text.String(), usage, lastResult, errEvents, sessionID, start)
				if resp != nil {
					resp.TerminalOutcome = outcome
				}
				return resp, err
			}
			if outcomeErr == nil {
				parsed, err := ai.TerminalOutcomeFromEvent(ev)
				if err != nil {
					outcomeErr = err
				} else if parsed != nil {
					outcome = parsed
				}
			}
			switch ev.Kind {
			case ai.EventText:
				text.WriteString(ev.Text)
			case ai.EventResult:
				cp := ev
				lastResult = &cp
				if ev.Text != "" && text.Len() == 0 {
					text.WriteString(ev.Text)
				}
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

func finaliseCoalescedResponse(backend ai.Backend, model, text string, usage ai.Usage, lastResult *ai.Event, errEvents []ai.Event, sessionID string, start time.Time) (*ai.Response, error) {
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
		Backend:  backend,
		Usage:    usage,
		Duration: time.Since(start),
	}
	if lastResult != nil {
		resp.Raw = lastResult.Input
		// Carry the provider-reported cost (e.g. claude-cli total_cost_usd) so the
		// buffered Execute path does not lose it — buffered callers otherwise fall
		// back to a list-price recompute (finding D4).
		resp.CostUSD = lastResult.CostUSD
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
