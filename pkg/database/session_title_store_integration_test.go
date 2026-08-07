package database

import (
	"testing"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetSessionTitlePrecedence(t *testing.T) {
	handle := dbtest.ForT(t, dbtest.Options{Name: "captain_session_titles"})
	db, err := Open(t.Context(), WithDSN(handle.DSN()), WithMigrations())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	const (
		derivedTitle = "Update the category dimension on all accounts"
		aiTitle      = "Account dimension backfill"
		userTitle    = "FY25 dimension cleanup"
	)

	created, err := db.CreateOrGetSession(t.Context(), CreateSessionInput{
		Source: "aichat", Provider: "captain", HostID: "local",
	})
	require.NoError(t, err)
	require.Empty(t, created.Title, "an unnamed session must start without a title")

	derived, err := db.SetSessionTitle(t.Context(), SetSessionTitleInput{
		ID: created.ID, Title: "  " + derivedTitle + "\n", Source: SessionTitleDerived,
	})
	require.NoError(t, err)
	assert.Equal(t, derivedTitle, derived.Title, "a derived title fills a blank and is whitespace-collapsed")
	assert.Equal(t, string(SessionTitleDerived), derived.Metadata[SessionTitleSourceKey])
	assert.Equal(t, created.StateVersion, derived.StateVersion, "naming a session is not a state change")

	unchanged, err := db.SetSessionTitle(t.Context(), SetSessionTitleInput{
		ID: created.ID, Title: "a second guess", Source: SessionTitleDerived,
	})
	require.NoError(t, err)
	assert.Equal(t, derivedTitle, unchanged.Title, "a derived title must not overwrite an existing title")

	byAgent, err := db.SetSessionTitle(t.Context(), SetSessionTitleInput{
		ID: created.ID, Title: aiTitle, Source: SessionTitleAI,
	})
	require.NoError(t, err)
	assert.Equal(t, aiTitle, byAgent.Title, "the agent's own title replaces a derived one")

	renamed, err := db.SetSessionTitle(t.Context(), SetSessionTitleInput{
		ID: created.ID, Title: userTitle, Source: SessionTitleUser,
	})
	require.NoError(t, err)
	assert.Equal(t, userTitle, renamed.Title)

	afterRename, err := db.SetSessionTitle(t.Context(), SetSessionTitleInput{
		ID: created.ID, Title: "a later agent title", Source: SessionTitleAI,
	})
	require.NoError(t, err)
	assert.Equal(t, userTitle, afterRename.Title, "an agent must not rename what a person named")

	_, err = db.SetSessionTitle(t.Context(), SetSessionTitleInput{
		ID: created.ID, Title: "   ", Source: SessionTitleUser,
	})
	assert.ErrorIs(t, err, ErrInvalidSession, "a blank rename is rejected rather than clearing the title")
}
