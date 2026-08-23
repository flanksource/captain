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

	session, turn, run, modelCallID := createCallerToolRun(t, db)
	secretHash := sha256.Sum256([]byte("credential-secret"))
	credential, err := db.CreateCallerToolCredential(t.Context(), CreateCallerToolCredentialInput{
		SessionID: session.ID, PromptRunID: run.ID, Backend: api.BackendClaudeAgent,
		SecretHash: secretHash[:], Policy: map[string]api.ToolPolicy{"account_edit": api.ToolPolicyAsk},
	})
	require.NoError(t, err)
	assert.Equal(t, session.ID, credential.SessionID)
	assert.Equal(t, run.ID, credential.PromptRunID)
	require.NoError(t, db.ValidateCallerToolCredential(t.Context(), credential.ID))

	request, err := db.CreateToolApprovalRequest(t.Context(), CreateToolApprovalRequestInput{
		CredentialID: credential.ID, SessionID: session.ID, TurnID: turn.ID, PromptRunID: run.ID,
		ModelCallID: modelCallID, RequestedBy: "caller_tool",
		ToolCallID: "call-account-1", Tool: "account_edit",
		Input: map[string]any{"id": "acc-1"}, ExpiresAt: time.Now().Add(time.Minute),
	})
	require.NoError(t, err)
	assert.Equal(t, TurnRequestStatePending, request.State)
	wrongTurnID := uuid.New()
	_, err = db.ResolveToolApprovalRequest(t.Context(), ResolveToolApprovalRequestInput{
		SessionID: session.ID, RequestID: request.ID, ExpectedTurnID: &wrongTurnID, Approved: true,
	})
	assert.ErrorIs(t, err, ErrTurnRequestConflict)
	stillPending, err := db.GetTurnRequest(t.Context(), request.ID)
	require.NoError(t, err)
	assert.Equal(t, TurnRequestStatePending, stillPending.State)

	resolved, err := db.ResolveToolApprovalRequest(t.Context(), ResolveToolApprovalRequestInput{
		SessionID: session.ID, RequestID: request.ID, ExpectedTurnID: &turn.ID, Approved: true,
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
		SessionID: otherSession.ID, RequestID: request.ID, Approved: false,
	})
	assert.ErrorIs(t, err, ErrTurnRequestNotFound)

	second, err := db.CreateToolApprovalRequest(t.Context(), CreateToolApprovalRequestInput{
		CredentialID: credential.ID, SessionID: session.ID, TurnID: turn.ID, PromptRunID: run.ID,
		ModelCallID: modelCallID, RequestedBy: "caller_tool",
		ToolCallID: "call-account-2", Tool: "account_edit",
		Input: map[string]any{"id": "acc-2"}, ExpiresAt: time.Now().Add(time.Minute),
	})
	require.NoError(t, err)
	require.NoError(t, db.RevokeCallerToolCredential(t.Context(), credential.ID, "turn completed"))
	assert.ErrorIs(t, db.ValidateCallerToolCredential(t.Context(), credential.ID), ErrCallerToolCredentialInactive)
	_, err = db.ResolveToolApprovalRequest(t.Context(), ResolveToolApprovalRequestInput{
		SessionID: session.ID, RequestID: second.ID, Approved: true,
	})
	assert.ErrorIs(t, err, ErrCallerToolCredentialInactive)
}

func createCallerToolRun(t *testing.T, db *DB) (*Session, *ChatTurn, *PromptRun, uuid.UUID) {
	t.Helper()
	session, err := db.CreateOrGetSession(t.Context(), CreateSessionInput{
		ID: uuid.New(), Source: "aichat", Provider: "anthropic",
	})
	require.NoError(t, err)
	turn, created, err := db.CreateChatTurn(t.Context(), CreateChatTurnInput{
		SessionID: session.ID, ProviderTurnID: "user-message-1",
	})
	require.NoError(t, err)
	require.True(t, created)
	run, err := db.CreatePromptRun(t.Context(), CreatePromptRunInput{SessionID: session.ID, TurnID: &turn.ID})
	require.NoError(t, err)
	modelCallID, err := db.CreateChatModelCall(t.Context(), CreateChatModelCallInput{
		TurnID: turn.ID, PromptRunID: run.ID, Model: "sonnet", Backend: string(api.BackendClaudeAgent),
	})
	require.NoError(t, err)
	var currency string
	require.NoError(t, db.Gorm().Model(&modelCallRecord{}).
		Select("currency").Where("id = ?", modelCallID).Scan(&currency).Error)
	assert.Equal(t, "USD", currency)
	return session, turn, run, modelCallID
}
