package cli

import (
	"context"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/database"
)

// resultCostLookup resolves the result-derived cost captain recorded for the
// sessions it ran itself.
//
// A stored claude transcript carries no result record — no invocation summary,
// no running total — so replaying one can only rebuild the numbers from each
// response's usage and price them from the registry. That reconstruction is an
// estimate: it cannot see pricing the provider applied but never published
// (1-hour cache writes, for one), and on a real session it came out ~9% under
// the billed figure.
//
// Captain does hold the provider's own answer for any session it executed:
// pkg/aichat writes each EventResult's Usage and CostUSD to captain_model_calls.
// This looks that up so the replay surfaces report the billed figure rather than
// their own recomputation.
type resultCostLookup struct {
	db *database.DB
}

// newResultCostLookup opens the captain database, returning a lookup that
// reports nothing when no database is configured. Cost reporting over local
// transcripts must keep working without one, so an unavailable database
// degrades to the reconstruction rather than failing the command.
func newResultCostLookup(ctx context.Context) *resultCostLookup {
	db, err := captainDB(ctx)
	if err != nil || db == nil {
		return &resultCostLookup{}
	}
	return &resultCostLookup{db: db}
}

// resultCost is a session's recorded usage plus both readings of its cost: the
// resolved total, and the portion of it the provider actually reported.
type resultCost struct {
	Usage       api.Usage
	Model       string
	TotalUSD    float64
	ProviderUSD float64
}

// find returns the result captain recorded for a session identity (a provider
// session id or captain id).
//
// It reports a hit only when the stored rows carry a provider-reported cost.
// A row without one is not a result — it is another reconstruction, written by
// transcript ingest from the same per-message usage the caller already has, and
// possibly staler. Preferring it would trade a fresh estimate for an old one.
func (l *resultCostLookup) find(ctx context.Context, identity string) (resultCost, bool) {
	if l == nil || l.db == nil || identity == "" {
		return resultCost{}, false
	}
	overview, err := l.db.GetSessionOverviewByIdentity(ctx, identity)
	// An ambiguous identity (SessionConflictError) or a plain miss both mean
	// there is no single authoritative row to prefer.
	if err != nil || overview == nil {
		return resultCost{}, false
	}
	rows, err := l.db.ListThreadCosts(ctx, overview.ID)
	if err != nil || len(rows) == 0 {
		return resultCost{}, false
	}
	out := resultCost{Model: rows[0].Model}
	for i := range rows {
		out.Usage.InputTokens += int(rows[i].InputTokens)
		out.Usage.OutputTokens += int(rows[i].OutputTokens)
		out.Usage.ReasoningTokens += int(rows[i].ReasoningTokens)
		out.Usage.CacheReadTokens += int(rows[i].CacheReadTokens)
		out.Usage.CacheWriteTokens += int(rows[i].CacheWriteTokens)
		// total_cost already resolves provider-reported against list-price per
		// underlying call (see 67_view_session_costs.sql); provider_cost_usd is
		// how much of it the provider itself reported.
		out.TotalUSD += rows[i].TotalCost
		out.ProviderUSD += rows[i].ProviderCostUSD
	}
	return out, out.ProviderUSD > 0
}

// applyResultCosts replaces each session's reconstructed usage and cost with the
// figures captain recorded from the provider's results, where it has them.
// Sessions captain never ran keep their reconstruction, which stays marked as an
// estimate because no provider cost is set on it.
func applyResultCosts(ctx context.Context, sessions []claude.SessionCost) {
	if len(sessions) == 0 {
		return
	}
	lookup := newResultCostLookup(ctx)
	if lookup.db == nil {
		return
	}
	for i := range sessions {
		cost, ok := lookup.find(ctx, sessions[i].SessionID)
		if !ok {
			continue
		}
		sessions[i].Tokens = claude.TokenSummary{
			InputTokens:      cost.Usage.InputTokens,
			OutputTokens:     cost.Usage.OutputTokens,
			CacheWriteTokens: cost.Usage.CacheWriteTokens,
			CacheReadTokens:  cost.Usage.CacheReadTokens,
			TotalCost:        cost.TotalUSD,
			ProviderCostUSD:  cost.ProviderUSD,
		}
		if sessions[i].Model == "" {
			sessions[i].Model = cost.Model
		}
	}
}
