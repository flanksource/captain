package database

import (
	"crypto/sha256"
	"testing"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCallerToolCredentialReadsLegacyPolicySpelling pins the read side of the
// tool-vocabulary migration.
//
// The policy column is untyped jsonb with no check constraint and no migration
// rewrote it, so rows written before the vocabularies were unified still hold
// "on" — a value api.ToolPolicy does not recognise. Nothing validates on read,
// so without normalization such a row loads as ToolPolicy("on") and silently
// matches no policy comparison downstream.
//
// The row is written with raw SQL on purpose: CreateCallerToolCredential's
// validator rejects "on" now, which is exactly why an old row can only be
// reproduced by going around it.
func TestCallerToolCredentialReadsLegacyPolicySpelling(t *testing.T) {
	testDB := dbtest.ForT(t, dbtest.Options{Name: "captain_caller_tools_legacy"})
	db, err := Open(t.Context(), WithDSN(testDB.DSN()), WithMigrations())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	session, _, run, _ := createCallerToolRun(t, db)
	secretHash := sha256.Sum256([]byte("legacy-credential-secret"))
	credential, err := db.CreateCallerToolCredential(t.Context(), CreateCallerToolCredentialInput{
		SessionID: session.ID, PromptRunID: run.ID, Provider: api.Anthropic.Name, Mode: api.ModeAgent,
		SecretHash: secretHash[:], Policy: map[string]api.ToolPolicy{"account_edit": api.ToolPolicyAsk},
	})
	require.NoError(t, err)

	require.NoError(t, db.gorm.WithContext(t.Context()).
		Exec(`UPDATE captain_session_mcp_credentials SET policy = ? WHERE id = ?`,
			`{"account_edit":"ask","account_read":"on"}`, credential.ID).Error)

	loaded, err := db.GetCallerToolCredential(t.Context(), credential.ID)
	require.NoError(t, err)

	assert.Equal(t, api.ToolPolicyAllow, loaded.Policy["account_read"],
		`a persisted "on" is a tool cleared to run unprompted and must read back as allow`)
	assert.Equal(t, api.ToolPolicyAsk, loaded.Policy["account_edit"],
		"ask is spelled the same in both vocabularies and must survive untouched")
}
