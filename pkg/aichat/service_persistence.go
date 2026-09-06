package aichat

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

func closeExecution(execution Execution) {
	if execution == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := execution.Close(ctx); err != nil {
		serviceLog.Errorf("close authoritative chat execution: %v", err)
	}
}

func (s *Service) persistIncoming(ctx context.Context, request ChatRequest, runtime api.Model) error {
	if request.ThreadID == "" || request.Trigger != "submit-message" || request.MessageID != "" || len(request.Messages) == 0 {
		return nil
	}
	last := request.Messages[len(request.Messages)-1]
	if !strings.EqualFold(last.Role, string(api.RoleUser)) {
		return nil
	}
	store, err := s.threads(ctx)
	if err != nil {
		return err
	}
	if candidates := runtime.Candidates(); len(candidates) == 1 {
		if err := store.SetRuntime(ctx, request.ThreadID, candidates[0]); err != nil {
			return err
		}
	}
	if err := store.AppendMessage(ctx, request.ThreadID, last); err != nil {
		return err
	}
	// Names an as-yet-unnamed thread after the message that opened it. The store
	// keeps this from displacing a title the agent or the user already chose.
	s.setThreadTitle(ctx, request.ThreadID, TitleUpdate{
		Title: derivedTitle(request.Messages), Source: TitleSourceDerived,
	})
	return nil
}

// persistEvent accrues a completed turn against its thread. The thread returned
// by AddUsage carries the conversation's running total, which is recorded on
// costs so the finish part can report cumulative rather than per-turn spend.
func (s *Service) persistEvent(ctx context.Context, threadID string, event api.Event, model api.Model, costs *TurnCosts) error {
	store, err := s.threads(ctx)
	if err != nil {
		return err
	}
	if event.SessionID != "" {
		if err := store.SetProviderSession(ctx, threadID, event.SessionID); err != nil {
			return fmt.Errorf("persist provider session: %w", err)
		}
	}
	if event.Kind != api.EventResult || event.Usage == nil {
		return nil
	}
	thread, err := store.AddUsage(ctx, threadID, TurnUsage{
		InputTokens: event.Usage.InputTokens, OutputTokens: event.Usage.OutputTokens,
		ReasoningTokens: event.Usage.ReasoningTokens, CacheReadTokens: event.Usage.CacheReadTokens,
		CacheWriteTokens: event.Usage.CacheWriteTokens, CostUSD: event.CostUSD,
	})
	if err != nil {
		return fmt.Errorf("persist thread usage: %w", err)
	}
	if costs != nil {
		costs.Breakdown = costBreakdownMetadata(model, *event.Usage, event.CostUSD)
		if thread != nil {
			costs.ThreadCostUSD = thread.TotalCostUSD
		}
	}
	return nil
}

func costBreakdownMetadata(model api.Model, usage api.Usage, providerCostUSD float64) *CostBreakdownMetadata {
	cost := ai.PriceUsage(model.Provider, model.Name, usage, providerCostUSD)
	return &CostBreakdownMetadata{
		Model:        cost.Model,
		InputUSD:     cost.InputCost,
		OutputUSD:    cost.OutputCost,
		ReasoningUSD: cost.ReasoningCost,
		CacheReadUSD: cost.CacheReadCost,
		// genkit reports no cache-write tokens on the API backends, so this
		// stays zero there rather than being silently omitted.
		CacheWriteUSD: cost.CacheWriteCost,
		TotalUSD:      cost.Total(),
	}
}
