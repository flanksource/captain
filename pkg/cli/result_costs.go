package cli

import (
	"context"
	"path/filepath"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/database"
)

// resultCostLookup resolves attributed provider cost or Claude Code's
// client-estimated session total for a transcript session.
//
// A stored claude transcript carries no result record — no invocation summary,
// no running total — so replaying one can only rebuild the numbers from each
// response's usage and price them from the registry. That reconstruction is an
// estimate: it cannot see pricing the provider applied but never published
// (1-hour cache writes, for one), and on a real session it came out ~9% under
// the billed figure.
//
// Claude Code's status-line total remains a whole-session CLI estimate rather
// than being promoted to provider billing or attributed to model calls.
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

// find returns the result Captain recorded for one Claude transcript.
//
// ParseCosts emits root transcripts only, so a root result must include model
// calls from every child in the thread. A separately supplied child transcript
// remains scoped to that child to avoid duplicating its cost. Reconstructed
// database buckets are not preferred over a fresh transcript parse unless the
// thread also contains a provider-reported cost.
func (l *resultCostLookup) find(ctx context.Context, identity, historyFile string) (resultCost, bool) {
	if l == nil || l.db == nil || identity == "" {
		return resultCost{}, false
	}
	overview, ok := l.findClaudeTranscript(ctx, identity, historyFile)
	if !ok {
		return resultCost{}, false
	}
	rootID := overview.ID
	if overview.RootSessionID != nil {
		rootID = *overview.RootSessionID
	}
	rows, err := l.db.ListThreadCosts(ctx, rootID)
	if err != nil {
		return resultCost{}, false
	}
	var threadProviderUSD float64
	for i := range rows {
		threadProviderUSD += rows[i].ProviderCostUSD
	}
	if threadProviderUSD > 0 {
		if overview.RootSessionID != nil {
			if overview.ProviderCostUSD <= 0 {
				return resultCost{}, false
			}
			out := resultCost{
				Usage: api.Usage{
					InputTokens:      int(overview.InputTokens),
					OutputTokens:     int(overview.OutputTokens),
					ReasoningTokens:  int(overview.ReasoningTokens),
					CacheReadTokens:  int(overview.CacheReadTokens),
					CacheWriteTokens: int(overview.CacheWriteTokens),
				},
				TotalUSD: overview.CostUSD, ProviderUSD: overview.ProviderCostUSD,
			}
			if overview.Model != nil {
				out.Model = *overview.Model
			}
			return out, true
		}
		out := resultCost{}
		for i := range rows {
			out.Usage.InputTokens += int(rows[i].InputTokens)
			out.Usage.OutputTokens += int(rows[i].OutputTokens)
			out.Usage.ReasoningTokens += int(rows[i].ReasoningTokens)
			out.Usage.CacheReadTokens += int(rows[i].CacheReadTokens)
			out.Usage.CacheWriteTokens += int(rows[i].CacheWriteTokens)
			out.TotalUSD += rows[i].TotalCost
			out.ProviderUSD += rows[i].ProviderCostUSD
			if out.Model == "" {
				out.Model = rows[i].Model
			}
		}
		return out, true
	}
	if overview.ClaudeCLICostUSD != nil && *overview.ClaudeCLICostUSD > 0 {
		return resultCost{TotalUSD: *overview.ClaudeCLICostUSD}, true
	}
	return resultCost{}, false
}

func (l *resultCostLookup) findClaudeTranscript(ctx context.Context, identity, historyFile string) (*database.SessionOverview, bool) {
	rows, err := l.db.ListSessionOverviewsByProviderSessionID(ctx, identity)
	if err != nil {
		return nil, false
	}
	var match *database.SessionOverview
	for i := range rows {
		if rows[i].Source != "claude" || !overviewMatchesHistoryFile(rows[i], historyFile) {
			continue
		}
		if match != nil {
			return nil, false
		}
		match = &rows[i]
	}
	return match, match != nil
}

func overviewMatchesHistoryFile(overview database.SessionOverview, historyFile string) bool {
	if historyFile == "" {
		return true
	}
	want := filepath.Clean(historyFile)
	return (overview.HistoryFile != nil && filepath.Clean(*overview.HistoryFile) == want) ||
		(overview.Path != nil && filepath.Clean(*overview.Path) == want)
}

// applyResultCosts prefers stored provider-attributed calls, then a positive
// Claude CLI estimate. The estimate replaces only TotalCost, preserving
// transcript usage and a zero ProviderCostUSD so output remains estimated and
// carries the Claude CLI label.
func applyResultCosts(ctx context.Context, sessions []claude.SessionCost) {
	if len(sessions) == 0 {
		return
	}
	lookup := newResultCostLookup(ctx)
	if lookup.db == nil {
		return
	}
	for i := range sessions {
		cost, ok := lookup.find(ctx, sessions[i].SessionID, sessions[i].HistoryFile)
		if !ok {
			continue
		}
		if cost.ProviderUSD <= 0 {
			sessions[i].Tokens.TotalCost = cost.TotalUSD
			sessions[i].CostSource = costSourceClaudeCLI
			continue
		}
		sessions[i].CostSource = costSourceProvider
		if cost.TotalUSD-cost.ProviderUSD > 1e-9 {
			sessions[i].CostSource = costSourceMixed
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
