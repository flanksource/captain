package aichat

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/google/uuid"
)

// ThreadCosts is the cost breakdown for one chat thread, scoped to the root
// session plus every subagent session beneath it.
//
// ByModel is the meaningful axis today: a chat thread can switch backends
// mid-conversation (an API turn followed by an agent turn bills at very
// different rates), and only this split makes that visible. ByAgent carries the
// sub-session dimension, which collapses to a single root row until chat
// threads start recording subagents as child sessions.
type ThreadCosts struct {
	ThreadID     string                  `json:"threadId"`
	TotalCostUSD float64                 `json:"totalCostUsd"`
	ByModel      []database.SessionCost  `json:"byModel"`
	ByAgent      []database.SessionAgent `json:"byAgent"`
}

// ThreadCostReader is implemented by thread stores that can report a thread's
// cost breakdown. The in-memory store cannot, so the route reports that rather
// than serving zeros that look like a free conversation.
type ThreadCostReader interface {
	GetThreadCosts(context.Context, string) (*ThreadCosts, error)
}

func (s *DatabaseThreadStore) GetThreadCosts(ctx context.Context, id string) (*ThreadCosts, error) {
	rootID, err := uuid.Parse(strings.TrimSpace(id))
	if err != nil {
		return nil, fmt.Errorf("captain chat session ID %q is not a UUID: %w", id, err)
	}
	costs, err := s.db.ListThreadCosts(ctx, rootID)
	if err != nil {
		return nil, err
	}
	agents, err := s.db.ListThreadAgents(ctx, rootID)
	if err != nil {
		return nil, err
	}
	return &ThreadCosts{
		ThreadID:     rootID.String(),
		TotalCostUSD: threadTotalCostUSD(agents),
		ByModel:      costs,
		ByAgent:      agents,
	}, nil
}

// threadTotalCostUSD sums every session in the thread rather than reading the
// root's own total: captain_session_overview scopes cost to `t.session_id =
// s.id`, so a root row's figure excludes its subagents' spend.
func threadTotalCostUSD(agents []database.SessionAgent) float64 {
	var total float64
	for i := range agents {
		total += agents[i].CostUSD
	}
	return total
}

// applyThreadCosts replaces a session aggregate's root-scoped usage and cost
// with thread-wide totals, and fills the per-model breakdown that the DB path
// otherwise leaves empty.
func applyThreadCosts(aggregate *session.Session, rows []database.SessionCost) {
	if len(rows) == 0 {
		return
	}
	costs := make(api.Costs, len(rows))
	for i := range rows {
		costs[i] = api.Cost{
			Model:            rows[i].Model,
			InputTokens:      int(rows[i].InputTokens),
			OutputTokens:     int(rows[i].OutputTokens),
			ReasoningTokens:  int(rows[i].ReasoningTokens),
			CacheReadTokens:  int(rows[i].CacheReadTokens),
			CacheWriteTokens: int(rows[i].CacheWriteTokens),
			TotalTokens:      int(rows[i].TotalTokens),
			InputCost:        rows[i].InputCost,
			OutputCost:       rows[i].OutputCost,
			ReasoningCost:    rows[i].ReasoningCost,
			CacheReadCost:    rows[i].CacheReadCost,
			CacheWriteCost:   rows[i].CacheWriteCost,
			// Only the providers' own reported share, not the view's TotalCost:
			// TotalCost falls back to the list-priced buckets per call, so passing
			// it here would make every reconstruction claim to be a billed figure.
			// Cost.Total() resolves the two exactly as the view's CASE does.
			ProviderCostUSD: rows[i].ProviderCostUSD,
		}
	}
	total := costs.Sum()
	aggregate.Cost = total
	aggregate.Usage = api.Usage{
		InputTokens: total.InputTokens, OutputTokens: total.OutputTokens,
		ReasoningTokens: total.ReasoningTokens, CacheReadTokens: total.CacheReadTokens,
		CacheWriteTokens: total.CacheWriteTokens,
	}
	byModel := costs.ByModel()
	aggregate.ToolCosts = make(api.Costs, 0, len(byModel))
	for model, cost := range byModel {
		cost.Model = model
		aggregate.ToolCosts = append(aggregate.ToolCosts, cost)
	}
	sort.Slice(aggregate.ToolCosts, func(i, j int) bool {
		return aggregate.ToolCosts[i].Model < aggregate.ToolCosts[j].Model
	})
}

func applyThreadSummaryCosts(thread *Thread, rows []database.SessionCost) {
	aggregate := &session.Session{}
	applyThreadCosts(aggregate, rows)
	thread.TotalInputTokens = aggregate.Usage.InputTokens
	thread.TotalOutputTokens = aggregate.Usage.OutputTokens
	thread.TotalReasoningTokens = aggregate.Usage.ReasoningTokens
	thread.TotalCacheReadTokens = aggregate.Usage.CacheReadTokens
	thread.TotalCacheWriteTokens = aggregate.Usage.CacheWriteTokens
	thread.TotalCostUSD = aggregate.Cost.Total()
}
