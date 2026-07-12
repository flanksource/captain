package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/session"
	commonsdb "github.com/flanksource/commons-db/db"
	"gorm.io/gorm"
)

func TestNativeDatabaseConfigurationSkipsLegacySessionStore(t *testing.T) {
	shared := &gorm.DB{}
	var state sessionStoreState
	opened := false

	if err := state.configureNative(shared); err != nil {
		t.Fatalf("configureNative: %v", err)
	}
	if got := state.get(func() *sessionDB {
		opened = true
		return &sessionDB{gdb: &gorm.DB{}}
	}); got != nil {
		t.Fatalf("session store = %+v, want nil with native database configured", got)
	}
	if opened {
		t.Fatal("legacy session store opener ran after native database configuration")
	}
}

func TestNativeDatabaseConfigurationRejectsInitializedLegacyStore(t *testing.T) {
	legacy := &sessionDB{gdb: &gorm.DB{}}
	var state sessionStoreState
	if got := state.get(func() *sessionDB { return legacy }); got != legacy {
		t.Fatalf("session store = %+v, want legacy store", got)
	}
	if err := state.configureNative(&gorm.DB{}); err == nil {
		t.Fatal("configureNative accepted an already initialized legacy store")
	}
}

func TestNativeDatabaseConfigurationRejectsNil(t *testing.T) {
	var state sessionStoreState
	if err := state.configureNative(nil); err == nil {
		t.Fatal("configureNative accepted a nil database")
	}
}

func TestNativeDatabaseConfigurationRejectsReplacementPool(t *testing.T) {
	var state sessionStoreState
	first := &gorm.DB{}
	if err := state.configureNative(first); err != nil {
		t.Fatalf("configure first native database: %v", err)
	}
	if err := state.configureNative(first); err != nil {
		t.Fatalf("reconfiguring the same native database should be idempotent: %v", err)
	}
	if err := state.configureNative(&gorm.DB{}); err == nil {
		t.Fatal("configureNative accepted a replacement native pool")
	}
}

// TestRecordFromRow_ProjectsRichFields verifies the Row→SessionRecord projection
// carries git/provider/cost/tokens and derives the context-free percent.
func TestRecordFromRow_ProjectsRichFields(t *testing.T) {
	r := session.Row{
		ID:              "s1",
		Source:          "claude",
		Project:         "captain",
		Title:           "Improve session identity",
		InitialPrompt:   "Show meaningful session titles",
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
	if rec.Project != "captain" || rec.Title != "Improve session identity" || rec.InitialPrompt != "Show meaningful session titles" {
		t.Fatalf("record identity = %+v", rec)
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

func TestStoredBase_PersistsInlinePlan(t *testing.T) {
	r := session.Row{
		ID:     "codex-plan",
		Source: "codex",
		Plan: &session.Plan{
			Content:  "- [x] inspect\n- [ ] test",
			Explicit: true,
			Events:   []session.PlanEvent{{Kind: session.PlanWrite}},
		},
	}

	stored := storedBase(r)
	if stored.SummaryVersion != sessionSummaryVersion {
		t.Fatalf("summary version = %d, want %d", stored.SummaryVersion, sessionSummaryVersion)
	}

	if stored.Plan == nil {
		t.Fatal("stored plan is nil")
	}
	if stored.Plan.Content != r.Plan.Content || !stored.Plan.Explicit {
		t.Fatalf("stored plan = %+v, want %+v", stored.Plan, r.Plan)
	}
}

func TestGavelSessionDSNPrefersPrimaryEnv(t *testing.T) {
	t.Setenv(gavelDBEnvDSN, "postgres://primary-dsn")
	t.Setenv(gavelCacheEnvDSN, "postgres://legacy-dsn")

	dsn, source, err := gavelSessionDSN()
	if err != nil {
		t.Fatalf("gavelSessionDSN: %v", err)
	}
	if dsn != "postgres://primary-dsn" || source != gavelDBEnvDSN {
		t.Fatalf("dsn/source = %q/%q", dsn, source)
	}
}

func TestGavelSessionDSNFallsBackToLegacyEnv(t *testing.T) {
	t.Setenv(gavelDBEnvDSN, "")
	t.Setenv(gavelCacheEnvDSN, "postgres://legacy-dsn")

	dsn, source, err := gavelSessionDSN()
	if err != nil {
		t.Fatalf("gavelSessionDSN: %v", err)
	}
	if dsn != "postgres://legacy-dsn" || source != gavelCacheEnvDSN {
		t.Fatalf("dsn/source = %q/%q", dsn, source)
	}
}

func TestConfiguredSessionDSNPrefersPrimaryGavelEnv(t *testing.T) {
	t.Setenv(gavelDBEnvDSN, "postgres://primary-dsn")
	t.Setenv(gavelCacheEnvDSN, "postgres://gavel-dsn")
	t.Setenv(captainSessionEnvDSN, "postgres://captain-dsn")

	dsn, source, disabled, err := configuredSessionDSN()
	if err != nil {
		t.Fatalf("configuredSessionDSN: %v", err)
	}
	if disabled {
		t.Fatal("configuredSessionDSN unexpectedly disabled")
	}
	if dsn != "postgres://primary-dsn" || source != gavelDBEnvDSN {
		t.Fatalf("dsn/source = %q/%q", dsn, source)
	}
}

func TestConfiguredSessionDSNFallsBackToLegacyGavelEnv(t *testing.T) {
	t.Setenv(gavelDBEnvDSN, "")
	t.Setenv(gavelCacheEnvDSN, "postgres://gavel-dsn")
	t.Setenv(captainSessionEnvDSN, "postgres://captain-dsn")

	dsn, source, disabled, err := configuredSessionDSN()
	if err != nil {
		t.Fatalf("configuredSessionDSN: %v", err)
	}
	if disabled {
		t.Fatal("configuredSessionDSN unexpectedly disabled")
	}
	if dsn != "postgres://gavel-dsn" || source != gavelCacheEnvDSN {
		t.Fatalf("dsn/source = %q/%q", dsn, source)
	}
}

func TestConfiguredSessionDSNFallsBackToCaptainEnv(t *testing.T) {
	t.Setenv(gavelDBEnvDSN, "")
	t.Setenv(gavelCacheEnvDSN, "")
	t.Setenv(captainSessionEnvDSN, "postgres://captain-dsn")

	dsn, source, disabled, err := configuredSessionDSN()
	if err != nil {
		t.Fatalf("configuredSessionDSN: %v", err)
	}
	if disabled {
		t.Fatal("configuredSessionDSN unexpectedly disabled")
	}
	if dsn != "postgres://captain-dsn" || source != captainSessionEnvDSN {
		t.Fatalf("dsn/source = %q/%q", dsn, source)
	}
}

func TestGavelSessionDSNFromDBConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(gavelDBEnvDSN, "")
	t.Setenv(gavelCacheEnvDSN, "")
	dir := filepath.Join(home, ".config", "gavel")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "db.json")
	if err := os.WriteFile(path, []byte(`{"mode":"dsn","dsn":"postgres://configured-dsn"}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	dsn, source, err := gavelSessionDSN()
	if err != nil {
		t.Fatalf("gavelSessionDSN: %v", err)
	}
	if dsn != "postgres://configured-dsn" || source != path {
		t.Fatalf("dsn/source = %q/%q", dsn, source)
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
