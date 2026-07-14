package session

import (
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildSessionMetadataIgnoresReplayedClaudeBranch(t *testing.T) {
	entries := []claude.HistoryEntry{
		claudeTurnEntry("user-1", "2026-07-09T10:00:00Z", claude.MessageRoleUser, ""),
		claudeTurnEntry("assistant-1", "2026-07-09T10:00:05Z", claude.MessageRoleAssistant, claude.StopReasonEndTurn),
		claudeTurnEntry("user-2", "2026-07-09T11:00:00Z", claude.MessageRoleUser, ""),
		// Claude branch restoration can append an older, already recorded stop.
		claudeTurnEntry("assistant-1", "2026-07-09T10:00:05Z", claude.MessageRoleAssistant, claude.StopReasonEndTurn),
		claudeTurnEntry("assistant-2", "2026-07-09T11:00:05Z", claude.MessageRoleAssistant, claude.StopReasonEndTurn),
	}

	meta := buildSessionMetadata("claude", entries)
	require.Len(t, meta.turns, 2)
	assert.Equal(t, []string{"user-1", "assistant-1"}, meta.turns[0].MessageIDs)
	assert.Equal(t, []string{"user-2", "assistant-2"}, meta.turns[1].MessageIDs)
	assert.False(t, meta.turns[1].EndedAt.Before(*meta.turns[1].StartedAt))
	assert.Equal(t, "turn-1", meta.turnByEntry["assistant-1"])
}

func TestBuildSessionMetadataBoundsOutOfOrderUniqueTimestamps(t *testing.T) {
	entries := []claude.HistoryEntry{
		claudeTurnEntry("user", "2026-07-09T14:36:14Z", claude.MessageRoleUser, ""),
		claudeTurnEntry("assistant", "2026-07-09T13:37:53Z", claude.MessageRoleAssistant, claude.StopReasonEndTurn),
	}

	meta := buildSessionMetadata("claude", entries)
	require.Len(t, meta.turns, 1)
	require.NotNil(t, meta.turns[0].StartedAt)
	require.NotNil(t, meta.turns[0].EndedAt)
	assert.Equal(t, time.Date(2026, 7, 9, 13, 37, 53, 0, time.UTC), *meta.turns[0].StartedAt)
	assert.Equal(t, time.Date(2026, 7, 9, 14, 36, 14, 0, time.UTC), *meta.turns[0].EndedAt)
}

func claudeTurnEntry(id, timestamp string, role claude.MessageRole, stopReason claude.StopReason) claude.HistoryEntry {
	return claude.HistoryEntry{
		UUID:      id,
		Timestamp: timestamp,
		Message: claude.Message{
			Role:       role,
			StopReason: stopReason,
			Content:    []claude.ContentBlock{{Type: claude.ContentTypeText, Text: id}},
		},
	}
}
