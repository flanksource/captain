package database

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// legacyModelCall is one row of the vocabulary captain_model_calls.backend
// actually held, with the two axes recoverable from it. Four writers disagreed
// about what the column meant, so three of these shapes carry no mode at all —
// they must land as NULL rather than a fabricated "agent" that would be
// indistinguishable from an observed one forever after.
type legacyModelCall struct {
	backend  string
	provider string
	mode     string
}

// The composite vocabulary now exists only inside migrations 79 and 80, so this
// fixture recreates the legacy shape on a migrated database and applies the two
// scripts by hand: a fresh database is created with the new schema, and the
// hash-gated bundle will not re-run a script it has already recorded.
func TestRuntimeVocabularyMigrationSplitsEveryPersistedComposite(t *testing.T) {
	testDB := dbtest.ForT(t, dbtest.Options{Name: "captain_runtime_vocabulary"})
	db, err := Open(t.Context(), WithDSN(testDB.DSN()), WithMigrations())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	conn := db.Gorm().WithContext(t.Context())

	session, err := db.CreateOrGetSession(t.Context(), CreateSessionInput{
		ID: uuid.New(), Source: "aichat", Provider: "claude-agent", HostID: "local",
	})
	require.NoError(t, err)
	require.NoError(t, conn.Exec(`UPDATE captain_sessions SET metadata = jsonb_build_object(
		'aichat', true,
		'aichatRuntime', jsonb_build_object(
			'backend', 'claude-agent', 'model', 'claude-opus-5', 'effort', 'high'))
		WHERE id = ?`, session.ID).Error)

	calls := []legacyModelCall{
		{backend: "claude-agent", provider: "anthropic", mode: "agent"}, // aichat / prompt runs: an adapter id
		{backend: "anthropic", provider: "anthropic", mode: "api"},      // a provider name, which meant the API
		{backend: "codex", provider: "openai"},                          // pkg/monitor: a transcript source
		{backend: "unknown"},                                            // session ingest's blank fallback
		{backend: "legacy"},                                             // xero-cli's chat-session import
	}
	require.NoError(t, conn.Exec(`ALTER TABLE captain_model_calls ADD COLUMN backend text`).Error)
	turnID := uuid.New()
	require.NoError(t, conn.Exec(
		`INSERT INTO captain_turns (id, session_id, turn_index) VALUES (?, ?, 0)`, turnID, session.ID).Error)
	for index, call := range calls {
		require.NoError(t, conn.Exec(`INSERT INTO captain_model_calls (turn_id, call_index, model, backend)
			VALUES (?, ?, 'claude-opus-5', ?)`, turnID, index, call.backend).Error)
	}

	applyRuntimeVocabularyMigrations(t, conn)

	for index, call := range calls {
		var got struct {
			Provider *string
			Mode     *string
		}
		require.NoError(t, conn.Raw(
			`SELECT provider, mode FROM captain_model_calls WHERE call_index = ?`, index).Scan(&got).Error)
		require.Equal(t, call.provider, stringOrEmpty(got.Provider), "provider for backend %q", call.backend)
		require.Equal(t, call.mode, stringOrEmpty(got.Mode), "mode for backend %q", call.backend)
	}

	wantRuntime := map[string]any{
		"provider": "anthropic", "mode": "agent", "model": "claude-opus-5", "effort": "high",
	}
	require.Equal(t, wantRuntime, storedThreadRuntime(t, conn, session.ID))

	var provider string
	require.NoError(t, conn.Raw(`SELECT provider FROM captain_sessions WHERE id = ?`, session.ID).Row().Scan(&provider))
	require.Equal(t, "anthropic", provider, "a session provider that held an adapter id is normalized to the family")

	// Applying the scripts again must change nothing: 79 is guarded on the
	// dropped column and 80 coalesces onto the row's own keys.
	applyRuntimeVocabularyMigrations(t, conn)
	require.Equal(t, wantRuntime, storedThreadRuntime(t, conn, session.ID))

	// The write-once key is the reason every row must be rewritten in one
	// statement: SetSessionMetadataOnce compares the stored value with the one
	// the read path now produces, so a row the migration missed would not degrade
	// — it would fail this thread's very next turn, permanently.
	require.NoError(t, db.SetSessionMetadataOnce(t.Context(), session.ID, "aichatRuntime",
		PromptRunRuntimeSelection{Provider: "anthropic", Mode: "agent", Model: "claude-opus-5", Effort: "high"}))
}

// A backend no axis is recoverable from ("legacy" is xero-cli's import marker)
// would otherwise leave a model-only record behind, which binds nothing and
// conflicts with everything.
func TestRuntimeVocabularyMigrationUnbindsAnUnrecoverableThread(t *testing.T) {
	testDB := dbtest.ForT(t, dbtest.Options{Name: "captain_runtime_vocabulary_unbound"})
	db, err := Open(t.Context(), WithDSN(testDB.DSN()), WithMigrations())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	conn := db.Gorm().WithContext(t.Context())

	session, err := db.CreateOrGetSession(t.Context(), CreateSessionInput{
		ID: uuid.New(), Source: "aichat", Provider: "captain", HostID: "local",
	})
	require.NoError(t, err)
	require.NoError(t, conn.Exec(`UPDATE captain_sessions SET metadata = jsonb_build_object(
		'aichat', true,
		'aichatRuntime', jsonb_build_object('backend', 'legacy', 'model', 'claude-opus-5'))
		WHERE id = ?`, session.ID).Error)

	applyRuntimeVocabularyMigrations(t, conn)

	var present bool
	require.NoError(t, conn.Raw(
		`SELECT jsonb_exists(metadata, 'aichatRuntime') FROM captain_sessions WHERE id = ?`, session.ID).Row().Scan(&present))
	require.False(t, present, "an unrecoverable runtime record must be removed, not half-migrated")

	// Unbound is a state the thread recovers from: its next send binds it.
	require.NoError(t, db.SetSessionMetadataOnce(t.Context(), session.ID, "aichatRuntime",
		PromptRunRuntimeSelection{Provider: "anthropic", Mode: "agent", Model: "claude-opus-5"}))
}

func applyRuntimeVocabularyMigrations(t *testing.T, conn *gorm.DB) {
	t.Helper()
	for _, name := range []string{"79_runtime_provider_mode.sql", "80_runtime_metadata_mode.sql"} {
		script, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
		require.NoError(t, err)
		require.NoError(t, conn.Exec(string(script)).Error, name)
	}
}

func storedThreadRuntime(t *testing.T, conn *gorm.DB, sessionID uuid.UUID) map[string]any {
	t.Helper()
	var raw []byte
	require.NoError(t, conn.Raw(
		`SELECT metadata -> 'aichatRuntime' FROM captain_sessions WHERE id = ?`, sessionID).Row().Scan(&raw))
	runtime := map[string]any{}
	require.NoError(t, json.Unmarshal(raw, &runtime))
	return runtime
}

func stringOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
