package aichat

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/api"
)

// ExecutionRequest is the authoritative identity and resolved policy for one
// chat provider turn.
type ExecutionRequest struct {
	ThreadID                string
	RequestID               string
	Title                   string
	ExpectedThreadUpdatedAt time.Time
	Spec                    api.Spec
	Profile                 api.ResolvedSpec
	Definitions             []api.ToolDefinition
}

// ToolApprovalResolution is the authenticated user's answer to one live
// caller-tool approval.
type ToolApprovalResolution struct {
	ThreadID       string
	ApprovalID     string
	ExpectedTurnID string
	Approved       bool
	UpdatedInput   map[string]any
	Reason         string
}

type ApprovalContinuation struct {
	Execution Execution
	Spec      api.Spec
}

// ExecutionAuthority admits turns before provider launch and resolves approval
// decisions against the same durable identity.
type ExecutionAuthority interface {
	Begin(context.Context, ExecutionRequest) (Execution, error)
	ResolveToolApproval(context.Context, ToolApprovalResolution) (*ApprovalContinuation, error)
}

// Execution is one admitted provider turn. Its caller-tool endpoint is already
// bound to the Captain session and prompt run.
type Execution interface {
	CaptainSessionID() string
	TurnID() string
	PromptRunID() string
	CallerTools() *api.CallerToolEndpoint
	Events() <-chan api.Event
	Observe(context.Context, api.Event) (api.Event, error)
	Interrupt(context.Context, string) error
	Close(context.Context) error
}

type TerminalCommit struct {
	Event   api.Event
	Message UIMessage
	Replace bool
}

// TerminalExecution commits the assistant message and authoritative terminal
// execution state through one durable boundary.
type TerminalExecution interface {
	CommitTerminal(context.Context, TerminalCommit) error
}

// executionRuntimeBinder commits the provider runtime selected after fallback
// resolution, before any provider session identity from that runtime is stored.
type executionRuntimeBinder interface {
	BindRuntime(context.Context, api.Model) error
}

func mergeExecutionEvents(
	ctx context.Context,
	provider <-chan api.Event,
	approvals <-chan api.Event,
	definitions []api.ToolDefinition,
) <-chan api.Event {
	if approvals == nil {
		return provider
	}
	askTools := make(map[string]bool)
	for _, definition := range definitions {
		askTools[definition.Name] = definition.NeedsApproval()
	}
	out := make(chan api.Event)
	go func() {
		defer close(out)
		awaiting := make(map[string]bool)
		pendingApprovals := make(map[string]api.Event)
		deferred := make([]api.Event, 0)
		send := func(event api.Event) bool {
			select {
			case out <- event:
				return true
			case <-ctx.Done():
				return false
			}
		}
		flush := func() bool {
			for _, event := range deferred {
				if !send(event) {
					return false
				}
			}
			deferred = deferred[:0]
			return true
		}
		for provider != nil || (len(awaiting) > 0 && approvals != nil) {
			select {
			case <-ctx.Done():
				return
			case approval, ok := <-approvals:
				if !ok {
					approvals = nil
					continue
				}
				if !awaiting[approval.ToolCallID] {
					pendingApprovals[approval.ToolCallID] = approval
					continue
				}
				if !send(approval) {
					return
				}
				delete(awaiting, approval.ToolCallID)
				if len(awaiting) == 0 && !flush() {
					return
				}
			case event, ok := <-provider:
				if !ok {
					provider = nil
					continue
				}
				if event.Kind == api.EventToolUse {
					if !send(event) {
						return
					}
					if askTools[event.Tool] {
						awaiting[event.ToolCallID] = true
						if approval, ok := pendingApprovals[event.ToolCallID]; ok {
							if !send(approval) {
								return
							}
							delete(pendingApprovals, event.ToolCallID)
							delete(awaiting, event.ToolCallID)
						}
					}
					continue
				}
				if len(awaiting) > 0 {
					deferred = append(deferred, event)
					continue
				}
				if !send(event) {
					return
				}
			}
		}
		if len(awaiting) == 0 {
			_ = flush()
		}
	}()
	return out
}

func observeExecutionEvents(
	ctx context.Context,
	execution Execution,
	source <-chan api.Event,
) <-chan api.Event {
	if execution == nil {
		return source
	}
	out := make(chan api.Event)
	go func() {
		defer close(out)
		for event := range source {
			if _, atomic := execution.(TerminalExecution); atomic && isExecutionTerminalEvent(event) {
				if !sendEvent(ctx, out, event) {
					return
				}
				continue
			}
			observed, err := execution.Observe(ctx, event)
			if err != nil {
				failure := api.Event{
					Kind: api.EventError, Error: err.Error(), Model: event.Model, SessionID: event.SessionID,
				}
				if event.Kind == api.EventError {
					if event.Error != "" {
						failure.Error = errors.Join(errors.New(event.Error), err).Error()
					}
				} else if _, atomic := execution.(TerminalExecution); atomic {
					// The persistence stage will atomically commit this synthetic
					// terminal event with its assistant message.
				} else if observedTerminal, terminalErr := execution.Observe(ctx, failure); terminalErr != nil {
					failure.Error = errors.Join(err, fmt.Errorf("finish authoritative execution: %w", terminalErr)).Error()
				} else {
					failure = observedTerminal
				}
				sendEvent(ctx, out, failure)
				return
			}
			if !sendEvent(ctx, out, observed) {
				return
			}
		}
	}()
	return out
}
