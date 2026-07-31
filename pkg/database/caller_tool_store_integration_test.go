package database

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCallerToolCredentialAndApprovalLifecycle(t *testing.T) {
	testDB := dbtest.ForT(t, dbtest.Options{Name: "captain_caller_tools"})
	db, err := Open(t.Context(), WithDSN(testDB.DSN()), WithMigrations())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	session, run := createCallerToolRun(t, db)
	secretHash := sha256.Sum256([]byte("credential-secret"))
	credential, err := db.CreateCallerToolCredential(t.Context(), CreateCallerToolCredentialInput{
		SessionID: session.ID, PromptRunID: run.ID, Backend: api.BackendClaudeAgent,
		SecretHash: secretHash[:], Policy: map[string]api.ToolMode{"account_edit": api.ToolModeAsk},
	})
	require.NoError(t, err)
	assert.Equal(t, session.ID, credential.SessionID)
	assert.Equal(t, run.ID, credential.PromptRunID)
	require.NoError(t, db.ValidateCallerToolCredential(t.Context(), credential.ID))

	request, err := db.CreateToolApprovalRequest(t.Context(), CreateToolApprovalRequestInput{
		CredentialID: credential.ID, SessionID: session.ID, PromptRunID: run.ID,
		ToolCallID: "call-account-1", Tool: "account_edit",
		Input: map[string]any{"id": "acc-1"}, ExpiresAt: time.Now().Add(time.Minute),
	})
	require.NoError(t, err)
	assert.Equal(t, TurnRequestStatePending, request.State)

	resolved, err := db.ResolveToolApprovalRequest(t.Context(), ResolveToolApprovalRequestInput{
		SessionID: session.ID, ToolCallID: "call-account-1", Approved: true,
		UpdatedInput: map[string]any{"id": "acc-1", "name": "Receivables"}, ResolvedBy: "chat",
	})
	require.NoError(t, err)
	assert.Equal(t, TurnRequestStateApproved, resolved.State)
	assert.Equal(t, "Receivables", resolved.Response["updatedInput"].(map[string]any)["name"])

	otherSession, err := db.CreateOrGetSession(t.Context(), CreateSessionInput{
		ID: uuid.New(), Source: "aichat", Provider: "anthropic",
	})
	require.NoError(t, err)
	_, err = db.ResolveToolApprovalRequest(t.Context(), ResolveToolApprovalRequestInput{
		SessionID: otherSession.ID, ToolCallID: "call-account-1", Approved: false,
	})
	assert.ErrorIs(t, err, ErrTurnRequestNotFound)

	_, err = db.CreateToolApprovalRequest(t.Context(), CreateToolApprovalRequestInput{
		CredentialID: credential.ID, SessionID: session.ID, PromptRunID: run.ID,
		ToolCallID: "call-account-2", Tool: "account_edit",
		Input: map[string]any{"id": "acc-2"}, ExpiresAt: time.Now().Add(time.Minute),
	})
	require.NoError(t, err)
	require.NoError(t, db.RevokeCallerToolCredential(t.Context(), credential.ID, "turn completed"))
	assert.ErrorIs(t, db.ValidateCallerToolCredential(t.Context(), credential.ID), ErrCallerToolCredentialInactive)
	_, err = db.ResolveToolApprovalRequest(t.Context(), ResolveToolApprovalRequestInput{
		SessionID: session.ID, ToolCallID: "call-account-2", Approved: true,
	})
	assert.ErrorIs(t, err, ErrCallerToolCredentialInactive)
}

func createCallerToolRun(t *testing.T, db *DB) (*Session, *PromptRun) {
	t.Helper()
	session, err := db.CreateOrGetSession(t.Context(), CreateSessionInput{
		ID: uuid.New(), Source: "aichat", Provider: "anthropic",
	})
	require.NoError(t, err)
	run, err := db.CreatePromptRun(t.Context(), CreatePromptRunInput{SessionID: session.ID})
	require.NoError(t, err)
	return session, run
}
