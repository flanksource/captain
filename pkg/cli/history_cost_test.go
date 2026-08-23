package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestRunHistory_CostWithoutClaudeFlag is the F5 regression: `history --cost`
// with the default (both-source) scope must still compute per-row cost. The old
// code only took the token-linked path when Codex was explicitly out of scope
// (`showClaude && !showCodex`), so a bare `--cost` left the Cost column blank.
func TestRunHistory_CostWithoutClaudeFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "work", "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)

	sessionFile := filepath.Join(home, ".claude", "projects", claude.NormalizePath(project), "sess-cost.jsonl")
	writeJSONL(t, sessionFile, map[string]any{
		"type":      "assistant",
		"sessionId": "sess-cost",
		"uuid":      "a1",
		"timestamp": "2026-06-01T10:00:01Z",
		"cwd":       project,
		"message": map[string]any{
			"role":  "assistant",
			"model": "claude-opus-4-5",
			"usage": map[string]any{"input_tokens": 1000, "output_tokens": 500},
			"content": []any{map[string]any{
				"type":  "tool_use",
				"id":    "tu-1",
				"name":  "Read",
				"input": map[string]any{"file_path": "README.md"},
			}},
		},
	})

	// Cost:true, no Claude/Codex filter → both sources in scope (reproduces F5).
	out, err := RunHistory(HistoryOptions{
		Cost:   true,
		Since:  time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		Limit:  100,
		Agents: true,
	})
	if err != nil {
		t.Fatalf("RunHistory: %v", err)
	}
	result, ok := out.(session.HistoryResult)
	if !ok {
		t.Fatalf("RunHistory returned %T, want HistoryResult", out)
	}
	if len(result.Results) == 0 {
		t.Fatal("no history rows returned")
	}
	var withCost int
	for _, row := range result.Results {
		if row.Cost != "" {
			withCost++
		}
	}
	if withCost == 0 {
		t.Errorf("no row carried a cost with --cost and default (both-source) scope; F5 regression")
	}
}

func TestResultCostLookupThreadProviderPriority(t *testing.T) {
	db := withTestCaptainDB(t)
	rootID, providerChildID, siblingID, estimateID, zeroID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	const (
		rootPath          = "/tmp/root-cost.jsonl"
		providerChildPath = "/tmp/provider-child-cost.jsonl"
		siblingPath       = "/tmp/sibling-cost.jsonl"
		estimatePath      = "/tmp/estimate-cost.jsonl"
		zeroPath          = "/tmp/zero-cost.jsonl"
	)
	for _, input := range []database.CreateSessionInput{
		{ID: rootID, ProviderSessionID: "root-cost", Source: "claude", HostID: "cost-test", Path: rootPath},
		{ID: providerChildID, ProviderSessionID: "provider-child-cost", Source: "claude", HostID: "cost-test", Path: providerChildPath, ParentSessionID: &rootID, RootSessionID: &rootID},
		{ID: siblingID, ProviderSessionID: "sibling-cost", Source: "claude", HostID: "cost-test", Path: siblingPath, ParentSessionID: &rootID, RootSessionID: &rootID},
		{ID: estimateID, ProviderSessionID: "estimate-cost", Source: "claude", HostID: "cost-test", Path: estimatePath},
		{ID: zeroID, ProviderSessionID: "zero-cost", Source: "claude", HostID: "cost-test", Path: zeroPath},
	} {
		_, err := db.CreateOrGetSession(t.Context(), input)
		require.NoError(t, err)
	}
	_, err := db.CreateOrGetSession(t.Context(), database.CreateSessionInput{
		ProviderSessionID: "estimate-cost", Source: "gavel", HostID: "cost-test", Path: "/tmp/gavel-estimate-cost.jsonl",
	})
	require.NoError(t, err)
	require.NoError(t, db.ReportClaudeCLICost(t.Context(), rootID, 0.2, time.Now().UTC()))
	require.NoError(t, db.ReportClaudeCLICost(t.Context(), estimateID, 0.3, time.Now().UTC()))
	require.NoError(t, db.ReportClaudeCLICost(t.Context(), zeroID, 0, time.Now().UTC()))

	turnID := uuid.New()
	require.NoError(t, db.Gorm().Exec(`
		INSERT INTO captain_turns (id, session_id, turn_index, status)
		VALUES (?, ?, 0, 'ended')`, turnID, providerChildID).Error)
	require.NoError(t, db.Gorm().Exec(`
		INSERT INTO captain_model_calls
		  (turn_id, call_index, model, backend, status, input_tokens, input_cost, provider_cost_usd, currency)
		VALUES (?, 0, 'claude-test', 'claude', 'succeeded', 10, 0.1, 0.75, 'USD')`, turnID).Error)

	lookup := resultCostLookup{db: db}
	cost, ok := lookup.find(t.Context(), "provider-child-cost", providerChildPath)
	require.True(t, ok)
	require.InDelta(t, 0.75, cost.ProviderUSD, 1e-9)
	require.InDelta(t, 0.75, cost.TotalUSD, 1e-9)
	for identity, path := range map[string]string{"root-cost": rootPath, "sibling-cost": siblingPath} {
		_, ok := lookup.find(t.Context(), identity, path)
		require.False(t, ok, "thread priority must not copy a whole-thread result onto %s", identity)
	}
	_, ok = lookup.find(t.Context(), "zero-cost", zeroPath)
	require.False(t, ok, "a zero CLI sample must not replace a fresh transcript estimate")
	cost, ok = lookup.find(t.Context(), "estimate-cost", estimatePath)
	require.True(t, ok, "the Claude transcript row must win over another source with the same provider ID")
	require.InDelta(t, 0.3, cost.TotalUSD, 1e-9)

	threadSessions := []claude.SessionCost{
		{SessionID: "root-cost", HistoryFile: rootPath, Tokens: claude.TokenSummary{TotalCost: 0.1}},
		{SessionID: "provider-child-cost", HistoryFile: providerChildPath, Tokens: claude.TokenSummary{TotalCost: 0.2}},
		{SessionID: "sibling-cost", HistoryFile: siblingPath, Tokens: claude.TokenSummary{TotalCost: 0.3}},
	}
	applyResultCosts(t.Context(), threadSessions)
	require.InDelta(t, 0.1, threadSessions[0].Tokens.TotalCost, 1e-9)
	require.InDelta(t, 0.75, threadSessions[1].Tokens.TotalCost, 1e-9)
	require.InDelta(t, 0.3, threadSessions[2].Tokens.TotalCost, 1e-9)
	require.InDelta(t, 1.15, threadSessions[0].Tokens.TotalCost+threadSessions[1].Tokens.TotalCost+threadSessions[2].Tokens.TotalCost, 1e-9)

	sessions := []claude.SessionCost{{
		SessionID:   "estimate-cost",
		Project:     "project",
		Files:       []string{"a.go", "b.go"},
		HistoryFile: estimatePath,
		Tokens:      claude.TokenSummary{TotalCost: 0.1},
	}}
	applyResultCosts(t.Context(), sessions)
	require.InDelta(t, 0.3, sessions[0].Tokens.TotalCost, 1e-9)
	require.Zero(t, sessions[0].Tokens.ProviderCostUSD)
	require.Equal(t, costSourceClaudeCLI, sessions[0].CostSource)
	require.Equal(t, "Claude CLI estimate", costSourceLabel(sessionCostSource(sessions[0])))

	mixed := groupSessions(append(sessions, claude.SessionCost{
		SessionID: "fresh-cost",
		Project:   "project",
		Tokens:    claude.TokenSummary{TotalCost: 0.1},
	}), "project")
	require.Len(t, mixed, 1)
	require.Equal(t, "Mixed cost sources", costSourceLabel(sessionCostSource(mixed[0])))
	require.True(t, costSourceEstimated(sessionCostSource(mixed[0])))

	allocated := groupSessions(sessions, "file")
	require.Len(t, allocated, 2)
	for _, row := range allocated {
		require.Equal(t, "Allocated session estimate", costSourceLabel(sessionCostSource(row)))
		require.True(t, costSourceEstimated(sessionCostSource(row)))
	}
}
