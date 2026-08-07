package database

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// texts flattens a transcript page to the text of each message's first part, in
// the order the store returned them.
func texts(t *testing.T, messages []TranscriptMessage) []string {
	t.Helper()
	out := make([]string, 0, len(messages))
	for _, message := range messages {
		var parts []struct {
			Text string `json:"text"`
		}
		require.NoError(t, json.Unmarshal(message.Parts, &parts))
		require.NotEmpty(t, parts, "message %s has no parts", message.ID)
		out = append(out, parts[0].Text)
	}
	return out
}

// TestNoticesSurviveALaterIngestPass is the defect the negative sequence space
// exists to prevent. Notices used to be written at MAX(sequence)+1, which is the
// transcript line number the file is about to grow into: the next ingest pass
// inserted that real line, hit the (session_id, sequence) conflict, and its
// converge step then overwrote the notice with the line's content. Both the
// notice and the collision were silent.
func TestNoticesSurviveALaterIngestPass(t *testing.T) {
	db := openIngestTestDB(t)
	modTime := time.Now().UTC().Truncate(time.Second)

	first := testIngestBatch(modTime, 2048)
	session, err := db.IngestTranscript(t.Context(), first)
	require.NoError(t, err)

	// A commit hook fires between the run's turns. Its timestamp sits between
	// line 5 and line 7, which is where it must come back out.
	between := modTime.Add(-30 * time.Second)
	require.NoError(t, db.PutSessionNotices(t.Context(), session.ID, []api.Notice{
		{At: between, Phase: "turn", Text: "[post-turn] committed abc1234: fix: the thing"},
	}))

	// The transcript then grows: the run keeps going and the ingester appends the
	// lines that land on the sequence numbers the notice would have squatted on.
	grown := testIngestBatch(modTime, 4096)
	grown.Messages = append(grown.Messages, IngestMessage{
		Sequence: 9, ProviderMessageID: "uuid-9", Role: "assistant",
		PartsJSON:  []byte(`[{"type":"text","text":"after the commit"}]`),
		SourceLine: 9, OccurredAt: &modTime,
	})
	grown.Source.LastEventKey = "uuid-9"
	_, err = db.IngestTranscript(t.Context(), grown)
	require.NoError(t, err)

	// The thread query is the path the dashboard streams from, and the only one
	// that orders the two halves of the sequence space against each other.
	messages, err := db.ListThreadTranscriptMessages(t.Context(), session.ID)
	require.NoError(t, err)

	got := texts(t, messages)
	assert.Contains(t, got, "[post-turn] committed abc1234: fix: the thing",
		"the notice was overwritten by a transcript line that landed on its sequence")
	assert.Contains(t, got, "after the commit",
		"the appended transcript line was dropped by a conflict with the notice")

	// Ordered by when things happened, not by which half of the sequence space
	// they came from: the notice belongs between the turn it followed and the
	// one that came after it.
	assert.Equal(t, []string{
		"hello",
		"hi",
		"[post-turn] committed abc1234: fix: the thing",
		"done",
		"after the commit",
		"next", // line 5 carries no timestamp, so it sorts last
	}, got)
}

// TestNoticesAreIdempotent covers a re-flushed workspace: a resumed run, or a
// retried write after a partial failure, must update its notices rather than
// stack a second copy of each.
func TestNoticesAreIdempotent(t *testing.T) {
	db := openIngestTestDB(t)
	modTime := time.Now().UTC().Truncate(time.Second)
	session, err := db.IngestTranscript(t.Context(), testIngestBatch(modTime, 2048))
	require.NoError(t, err)

	notices := []api.Notice{
		{At: modTime.Add(-2 * time.Minute), Phase: "turn", Text: "[post-turn] committing 3 file(s)"},
		{At: modTime.Add(-1 * time.Minute), Phase: "turn", Text: "[post-turn] committed abc1234: fix: it"},
	}
	require.NoError(t, db.PutSessionNotices(t.Context(), session.ID, notices))
	require.NoError(t, db.PutSessionNotices(t.Context(), session.ID, notices))

	messages, err := db.ListTranscriptMessages(t.Context(), TranscriptPage{SessionID: session.ID})
	require.NoError(t, err)

	var found int
	for _, text := range texts(t, messages) {
		if len(text) > 6 && text[:6] == "[post-" {
			found++
		}
	}
	assert.Equal(t, len(notices), found, "re-flushing the same notices duplicated them")
}

// TestEmptyNoticesWriteNothing keeps the flush call site free of guards: a run
// whose hooks said nothing is the common case.
func TestEmptyNoticesWriteNothing(t *testing.T) {
	db := openIngestTestDB(t)
	modTime := time.Now().UTC().Truncate(time.Second)
	session, err := db.IngestTranscript(t.Context(), testIngestBatch(modTime, 2048))
	require.NoError(t, err)

	require.NoError(t, db.PutSessionNotices(t.Context(), session.ID, nil))
	require.NoError(t, db.PutSessionNotices(t.Context(), session.ID, []api.Notice{{At: modTime, Text: "  "}}))

	messages, err := db.ListTranscriptMessages(t.Context(), TranscriptPage{SessionID: session.ID})
	require.NoError(t, err)
	assert.Len(t, messages, len(testIngestBatch(modTime, 2048).Messages))
}
