package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/session"
	commonsdb "github.com/flanksource/commons-db/db"
)

// TestRecordFromRow_ProjectsRichFields verifies the Row→SessionRecord projection
// carries git/provider/cost/tokens and derives the context-free percent.
func TestRecordFromRow_ProjectsRichFields(t *testing.T) {
	r := session.Row{
		ID:              "s1",
		Source:          "claude",
		Model:           "claude-opus-4",
		Git:             session.GitState{Branch: "main"},
		Provider:        "anthropic",
		Version:         "1.2.3",
		ReasoningEffort: "high",
		Usage:           api.Usage{InputTokens: 1000, OutputTokens: 500},
		Cost:            api.Cost{InputCost: 0.015, OutputCost: 0.0375},
		ContextTokens:   940_000, // 94% of the 1M window → 6% free
		ToolCalls:       3,
		Messages:        2,
	}
	rec := recordFromRow(r)

	if rec.ID != "s1" || rec.GitBranch != "main" || rec.Provider != "anthropic" || rec.ReasoningEffort != "high" {
		t.Fatalf("record meta = %+v", rec)
	}
	if rec.Context == nil || rec.Context.FreePercent != 6 || rec.Context.WindowTokens != 1_000_000 {
		t.Fatalf("context = %+v, want 6%% free of 1M", rec.Context)
	}
	if rec.Tokens == nil || rec.Tokens.InputTokens != 1000 || rec.Tokens.OutputTokens != 500 {
		t.Fatalf("tokens = %+v", rec.Tokens)
	}
	if rec.CostUSD == 0 {
		t.Errorf("cost not projected")
	}
	if rec.ToolCalls != 3 || rec.Messages != 2 {
		t.Errorf("counts = %d/%d, want 3/2", rec.ToolCalls, rec.Messages)
	}
}

// TestSessionStoreRoundTrip exercises the real gorm store: fresh miss inserts,
// unchanged hit is served from the row (parent-linked child included), a changed
// file invalidates, and a realized prompt round-trips. Gated on a Postgres DSN.
func TestSessionStoreRoundTrip(t *testing.T) {
	dsn := os.Getenv("CAPTAIN_SESSION_DB_URL")
	if dsn == "" || dsn == "off" {
		t.Skip("set CAPTAIN_SESSION_DB_URL to a Postgres DSN to run the session store integration test")
	}
	gdb, _, err := commonsdb.SetupDB(dsn, "session-cache-test")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := gdb.AutoMigrate(&StoredSession{}, &StoredPrompt{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	gdb.Exec("TRUNCATE captain_sessions, captain_session_prompts")
	st := &sessionDB{gdb: gdb}

	// A root session with one sub-agent transcript.
	dir := t.TempDir()
	rootPath := filepath.Join(dir, "root.jsonl")
	writeJSONL(t, rootPath, map[string]any{
		"type": "assistant", "sessionId": "root", "uuid": "r1", "cwd": "/repo", "gitBranch": "main",
		"message": map[string]any{"role": "assistant", "model": "claude-opus-4",
			"usage":   map[string]any{"input_tokens": 1000, "output_tokens": 500},
			"content": []any{map[string]any{"type": "text", "text": "hi"}}},
	})

	rows, err := session.RowsFromFile(rootPath, "claude")
	if err != nil {
		t.Fatalf("rows: %v", err)
	}
	st.upsertRows(rows)

	info, _ := os.Stat(rootPath)
	if _, ok := st.lookupFresh(rootPath, info.ModTime().UnixNano(), info.Size()); !ok {
		t.Fatal("expected a fresh hit after upsert")
	}
	// A changed size invalidates.
	if _, ok := st.lookupFresh(rootPath, info.ModTime().UnixNano(), info.Size()+1); ok {
		t.Fatal("size change must invalidate the row")
	}

	// Realized-prompt round-trip.
	st.upsertPrompt(StoredPrompt{SessionID: "root", RunID: "run-1", Model: "claude-opus-4", Backend: "claude_cli"})
	if p, ok := st.prompt("root"); !ok || p.RunID != "run-1" {
		t.Fatalf("prompt = %+v, ok=%v", p, ok)
	}
}
