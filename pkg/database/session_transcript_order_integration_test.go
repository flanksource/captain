package database

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flanksource/captain/pkg/api"
)

// Claude Code flushes a turn's lines out of order: the prompt can land in the
// file after the reply it produced, so line numbers (the ingest sequence) put the
// answer before the question. The transcript has to read in the order the
// messages happened.
func TestTranscriptReadsInOccurrenceOrderWhenLinesAreFlushedOutOfOrder(t *testing.T) {
	db := openIngestTestDB(t)
	modTime := time.Now().UTC().Truncate(time.Second)
	asked := modTime.Add(-3 * time.Minute)
	answered := asked.Add(30 * time.Second)
	batch := testIngestBatch(modTime, 1024)
	turn := 0
	batch.Messages = []IngestMessage{
		{Sequence: 4, ProviderMessageID: "reply", Role: "assistant", TurnIndex: &turn,
			PartsJSON: []byte(`[{"type":"text","text":"reply"}]`), SourceLine: 4, OccurredAt: &answered},
		{Sequence: 6, ProviderMessageID: "untimed", Role: "assistant", TurnIndex: &turn,
			PartsJSON: []byte(`[{"type":"text","text":"untimed"}]`), SourceLine: 6},
		{Sequence: 9, ProviderMessageID: "prompt", Role: "user", TurnIndex: &turn,
			PartsJSON: []byte(`[{"type":"text","text":"prompt"}]`), SourceLine: 9, OccurredAt: &asked},
	}
	session, err := db.IngestTranscript(t.Context(), batch)
	require.NoError(t, err)
	// A notice sits in the negative sequence half but happened last; it must not
	// pull the untimed line after it.
	require.NoError(t, db.PutSessionNotices(t.Context(), session.ID, []api.Notice{
		{At: modTime, Phase: "turn", Text: "notice"},
	}))

	all, err := db.ListTranscriptMessages(t.Context(), TranscriptPage{SessionID: session.ID})
	require.NoError(t, err)
	// The untimed line follows the latest timed line before it in the file.
	assert.Equal(t, []string{"prompt", "reply", "untimed", "notice"}, texts(t, all))

	page, err := db.ListTranscriptMessages(t.Context(), TranscriptPage{SessionID: session.ID, Offset: 1, Limit: 2})
	require.NoError(t, err)
	assert.Equal(t, []string{"reply", "untimed"}, texts(t, page))

	tail, err := db.ListTranscriptMessages(t.Context(), TranscriptPage{SessionID: session.ID, Tail: 3})
	require.NoError(t, err)
	assert.Equal(t, []string{"reply", "untimed", "notice"}, texts(t, tail))
}
