package aichat

import (
	"context"
	"fmt"
	"strings"
	"time"

	aitools "github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"
)

const (
	suspendedSeedTimeout  = 15 * time.Second
	suspendedSeedInterval = 25 * time.Millisecond
)

// awaitSuspendedSeed returns the assistant message the suspended turn ended on.
// The durable suspension (prompt run -> waiting) is committed while the same
// stream's persistence goroutine is still writing that assistant message, so an
// approval resolved the instant the run becomes resumable can observe the run
// before its transcript. Wait for that in-flight write instead of rejecting a
// legitimate approval, and fail loudly when it never lands.
func awaitSuspendedSeed(ctx context.Context, store ThreadStore, threadID, turnID string) (*UIMessage, error) {
	deadline := time.Now().Add(suspendedSeedTimeout)
	for {
		thread, err := store.Get(ctx, threadID)
		if err != nil {
			return nil, err
		}
		if len(thread.Messages) > 0 {
			seed := thread.Messages[len(thread.Messages)-1]
			if strings.EqualFold(seed.Role, string(api.RoleAssistant)) && seed.TurnID == turnID {
				return &seed, nil
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("captain chat session %s does not end with the suspended turn %s", threadID, turnID)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(suspendedSeedInterval):
		}
	}
}

func (s *Service) resumeToolApproval(ctx context.Context, threadID string, continuation *ApprovalContinuation) error {
	if continuation == nil || continuation.Execution == nil || continuation.Spec.ToolApproval == nil {
		return fmt.Errorf("tool approval continuation is incomplete")
	}
	execution := continuation.Execution
	defer closeExecution(execution)
	settings, err := s.runtimeSettings(ctx)
	if err != nil {
		return fmt.Errorf("load chat runtime settings: %w", err)
	}
	set, err := s.loadTools(ctx)
	if err != nil {
		return err
	}
	definitions, err := aitools.ResolveDefinitions(set.Definitions, continuation.Spec.ToolPreferences)
	if err != nil {
		return err
	}
	config := settings.ProviderConfig
	config.Model = continuation.Spec.Model
	config.Budget = continuation.Spec.Budget
	config.SessionID = continuation.Spec.SessionID
	config.CaptainSessionID = execution.CaptainSessionID()
	config.Tools = definitions
	config, err = s.prepareProviderConfig(ctx, config)
	if err != nil {
		return err
	}
	continuation.Spec.Model = config.Model
	provider, err := s.resolver.Provider(ctx, config)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := closeProvider(provider); closeErr != nil {
			serviceLog.Errorf("close approval continuation provider: %v", closeErr)
		}
	}()
	if len(definitions) > 0 {
		capability, ok := api.ProviderAs[api.ToolCapableProvider](provider)
		if !ok || !capability.SupportsCallerTools() {
			return fmt.Errorf("backend %q does not support caller tools", provider.GetBackend())
		}
	}
	store, err := s.threads(ctx)
	if err != nil {
		return err
	}
	seed, err := awaitSuspendedSeed(ctx, store, threadID, execution.TurnID())
	if err != nil {
		return err
	}
	request := ChatRequest{
		ID: threadID, ThreadID: threadID, Trigger: "submit-message", MessageID: seed.ID,
		Messages: []UIMessage{*seed}, ToolApproval: continuation.Spec.ToolApproval,
	}
	streamContext, cancel := context.WithCancel(ctx)
	defer cancel()
	events, err := provider.ExecuteStream(streamContext, continuation.Spec)
	if err != nil {
		return err
	}
	active := newActiveTurn(streamContext, provider, execution, cancel)
	if err := s.registerActiveTurn(threadID, active); err != nil {
		return err
	}
	defer s.unregisterActiveTurn(threadID, active)
	events = active.stream(events)
	events = observeExecutionEvents(streamContext, execution, events)
	// A resumed approval writes no SSE stream, so no TurnCosts sink is needed;
	// the thread total is recomputed from the database on the next read.
	resumed := s.persistedEvents(streamContext, persistedEventOptions{
		Request: request, TurnID: execution.TurnID(), Model: continuation.Spec.Model,
	}, events)
	for event := range resumed {
		if event.Kind == api.EventError {
			return fmt.Errorf("resume provider approval: %s", event.Error)
		}
	}
	return nil
}
