package database

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionMetadataWriteOnceAndTurnlessMessage(t *testing.T) {
	testDB := dbtest.ForT(t, dbtest.Options{Name: "captain_session_metadata_once"})
	db, err := Open(t.Context(), WithDSN(testDB.DSN()), WithMigrations())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	session, err := db.CreateOrGetSession(t.Context(), CreateSessionInput{
		ID: uuid.New(), Source: "aichat", Provider: "captain", HostID: "local",
	})
	require.NoError(t, err)
	runtime := map[string]any{"model": "test-model", "mode": "api"}
	require.NoError(t, db.SetSessionMetadataOnce(t.Context(), session.ID, "aichatRuntime", runtime))
	require.NoError(t, db.SetSessionMetadataOnce(t.Context(), session.ID, "aichatRuntime", map[string]any{
		"mode": "api", "model": "test-model",
	}))
	err = db.SetSessionMetadataOnce(t.Context(), session.ID, "aichatRuntime", map[string]any{
		"model": "other-model", "mode": "api",
	})
	assert.True(t, errors.Is(err, ErrSessionConflict))

	require.NoError(t, db.PutChatMessage(t.Context(), PutChatMessageInput{
		SessionID: session.ID, ProviderMessageID: "fork-seed", Role: "user",
		Parts: json.RawMessage(`[{
			"type":"data-fork-seed","data":{"forkedFrom":"source"}
		},{"type":"text","text":"seed"}]`),
	}))
	var turnID *uuid.UUID
	require.NoError(t, db.Gorm().WithContext(t.Context()).Raw(`
		SELECT turn_id FROM captain_messages
		WHERE session_id = ? AND provider_message_id = 'fork-seed'
	`, session.ID).Row().Scan(&turnID))
	assert.Nil(t, turnID)
}
