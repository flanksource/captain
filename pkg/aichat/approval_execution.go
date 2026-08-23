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

// resumeToolApproval reports whether it consumed the caller's pending
// reservation, even when the activated continuation later fails.
func (s *Service) resumeToolApproval(ctx context.Context, threadID string, continuation *ApprovalContinuation) (bool, error) {
	if continuation == nil || continuation.Execution == nil || continuation.Spec.ToolApproval == nil {
		return false, fmt.Errorf("tool approval continuation is incomplete")
	}
	execution := continuation.Execution
	defer closeExecution(execution)
	profile, err := s.runtimeProfile(ctx)
	if err != nil {
		return false, fmt.Errorf("load chat runtime profile: %w", err)
	}
	if err := enforceApprovalRuntimeProfile(continuation.Spec, profile.Resolved); err != nil {
		if interruptErr := execution.Interrupt(ctx, err.Error()); interruptErr != nil {
			return false, fmt.Errorf("%w (interrupt rejected approval continuation: %v)", err, interruptErr)
		}
		return false, err
	}
	set, err := s.loadTools(ctx)
	if err != nil {
		return false, err
	}
	definitions, err := aitools.ResolveDefinitions(set.Definitions, aitools.ResolveOptions{Preferences: continuation.Spec.ToolPreferences, Policy: continuation.Spec.ToolPolicy, Strategies: s.options.ToolStrategies})
	if err != nil {
		return false, err
	}
	config := profile.ProviderConfig
	config.Model = continuation.Spec.Model
	config.Budget = continuation.Spec.Budget
	config.SessionID = continuation.Spec.SessionID
	config.CaptainSessionID = execution.CaptainSessionID()
	config.Tools = definitions
	config, err = s.prepareProviderConfig(ctx, config)
	if err != nil {
		return false, err
	}
	continuation.Spec.Model = config.Model
	provider, err := s.resolver.Provider(ctx, config)
	if err != nil {
		return false, err
	}
	defer func() {
		if closeErr := closeProvider(provider); closeErr != nil {
			serviceLog.Errorf("close approval continuation provider: %v", closeErr)
		}
	}()
	if len(definitions) > 0 {
		capability, ok := api.ProviderAs[api.ToolCapableProvider](provider)
		if !ok || !capability.SupportsCallerTools() {
			return false, fmt.Errorf("backend %q does not support caller tools", provider.GetBackend())
		}
	}
	seed, err := s.awaitSuspendedSeed(ctx, threadID, execution.TurnID())
	if err != nil {
		return false, err
	}
	request := ChatRequest{
		ID: threadID, ThreadID: threadID, Trigger: "submit-message", MessageID: seed.ID,
		Messages: []UIMessage{*seed}, ToolApproval: continuation.Spec.ToolApproval,
	}
	streamContext, cancel := context.WithCancel(ctx)
	defer cancel()
	active := newActiveTurn(streamContext, provider, execution, cancel)
	if err := s.registerActiveTurn(threadID, active); err != nil {
		return false, err
	}
	defer s.unregisterActiveTurn(threadID, active)
	events, err := provider.ExecuteStream(streamContext, continuation.Spec)
	if err != nil {
		return true, err
	}
	runtime := func() api.Model { return providerRuntime(provider, config.Model) }
	events = s.bindThreadRuntime(streamContext, threadID, execution, runtime, events)
	events = active.stream(events)
	events = observeExecutionEvents(streamContext, execution, events)
	// A resumed approval writes no SSE stream, so no TurnCosts sink is needed;
	// the thread total is recomputed from the database on the next read.
	resumed := s.persistedEvents(streamContext, persistedEventOptions{
		Request: request, TurnID: execution.TurnID(), Model: continuation.Spec.Model, Runtime: runtime,
		terminalMetadata: terminalMetadataContext{
			CaptainSessionID:  execution.CaptainSessionID(),
			ProviderSessionID: config.SessionID,
			ThreadID:          threadID,
			TurnID:            execution.TurnID(),
			Runtime:           runtime,
		},
	}, events)
	for event := range resumed {
		if event.Kind == api.EventError {
			return true, fmt.Errorf("resume provider approval: %s", event.Error)
		}
	}
	return true, nil
}

func enforceApprovalRuntimeProfile(spec api.Spec, resolved api.ResolvedSpec) error {
	if err := enforceRuntimeQuotas(resolved); err != nil {
		return err
	}
	if !resolved.AllowsModel(spec.Model) {
		return fmt.Errorf("approval continuation model %q is outside the current effective model catalog", spec.Name)
	}
	for _, fallback := range spec.Fallbacks {
		if !resolved.AllowsModel(fallback) {
			return fmt.Errorf("approval continuation fallback model %q is outside the current effective model catalog", fallback.Name)
		}
	}
	return nil
}

// suspendedSeedWait bounds how long an approval resolution waits for the
// suspending turn's assistant message to land in the thread store.
const suspendedSeedWait = 5 * time.Second

// awaitSuspendedSeed returns the thread's trailing assistant message for the
// suspended turn. The prompt run reaches its waiting state from the event
// pipeline before the suspending stream persists that message on its final
// unwind, so an approval resolved from a session poll can arrive while the
// write is still in flight. The run's durable approval state guarantees the
// message is committed or imminent — wait it out instead of failing a
// resolution that has already consumed the approval.
func (s *Service) awaitSuspendedSeed(ctx context.Context, threadID, turnID string) (*UIMessage, error) {
	store, err := s.threads(ctx)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(suspendedSeedWait)
	for {
		thread, err := store.Get(ctx, threadID)
		if err != nil {
			return nil, err
		}
		if count := len(thread.Messages); count > 0 {
			seed := thread.Messages[count-1]
			if strings.EqualFold(seed.Role, string(api.RoleAssistant)) && seed.TurnID == turnID {
				return &seed, nil
			}
		}
		if time.Now().After(deadline) {
			if len(thread.Messages) == 0 {
				return nil, fmt.Errorf("captain chat session %s has no suspended assistant message", threadID)
			}
			return nil, fmt.Errorf("captain chat session %s does not end with the suspended turn %s", threadID, turnID)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
