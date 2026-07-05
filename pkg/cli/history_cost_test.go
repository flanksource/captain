package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/session"
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
			"model": "claude-opus-4",
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
