package database

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	commonsdb "github.com/flanksource/commons-db/db"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testProviderSessionID = "0195c1de-4ab8-7000-8000-0123456789ab"
	testTranscriptPath    = "/home/dev/.claude/projects/example/session.jsonl"
	testParserVersion     = 1
)

func openIngestTestDB(t *testing.T) *DB {
	t.Helper()
	if os.Getenv("CAPTAIN_DB_EMBEDDED_TEST") == "" {
		t.Skip("set CAPTAIN_DB_EMBEDDED_TEST=1 to run embedded-postgres store tests")
	}
	dsn, stop, err := commonsdb.StartEmbedded(commonsdb.EmbeddedConfig{
		DataDir:  filepath.Join(t.TempDir(), "postgres"),
		Database: "captain_ingest_stores",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stop()) })

	db, err := Open(t.Context(), Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func testIngestBatch(modTime time.Time, byteOffset int64) IngestTranscriptInput {
	started := modTime.Add(-10 * time.Minute)
	turn0End := started.Add(1 * time.Minute)
	turnIdx0, turnIdx1 := 0, 1
	return IngestTranscriptInput{
		Session: IngestSessionInput{
			ProviderSessionID: testProviderSessionID,
			Source:            "claude",
			HostID:            "test-host",
			Path:              testTranscriptPath,
			Project:           "example",
			CWD:               "/home/dev/example",
			Title:             "Ingest fixture session",
			Slug:              "ingest-fixture",
			CLIVersion:        "2.1.0",
			StartedAt:         &started,
			LastActivityAt:    &modTime,
			Git:               map[string]any{"branch": "main"},
			Metadata:          map[string]any{"providerName": "Claude Code"},
		},
		Source: IngestSourceInput{
			SourceKind: "claude", Path: testTranscriptPath, SourceIdentity: testProviderSessionID,
			ParserVersion: testParserVersion, ByteOffset: byteOffset, ObservedSize: byteOffset,
			ObservedModTime: modTime, LastEventKey: "uuid-7",
		},
		Turns: []IngestTurn{
			{
				Index: 0, ProviderTurnID: "turn-0", Status: TurnStatusEnded,
				StartedAt: &started, EndedAt: &turn0End,
				Call: &IngestModelCall{
					Model: "claude-sonnet-5", Backend: "claude", InputTokens: 1000, OutputTokens: 200,
					CacheReadTokens: 5000, ContextTokens: 40000, ContextWindowTokens: 200000,
					InputCost: 0.003, OutputCost: 0.003,
				},
			},
			{
				Index: 1, ProviderTurnID: "turn-1", Status: TurnStatusEnded,
				StartedAt: &turn0End, EndedAt: &modTime,
				Call: &IngestModelCall{
					Model: "claude-sonnet-5", Backend: "claude", InputTokens: 2000, OutputTokens: 400,
					CacheReadTokens: 8000, ContextTokens: 60000, ContextWindowTokens: 200000,
					InputCost: 0.006, OutputCost: 0.006,
				},
			},
		},
		Messages: []IngestMessage{
			{Sequence: 1, ProviderMessageID: "uuid-1", Role: "user", TurnIndex: &turnIdx0,
				PartsJSON: []byte(`[{"type":"text","text":"hello"}]`), SourceLine: 1, OccurredAt: &started},
			{Sequence: 3, ProviderMessageID: "uuid-3", Role: "assistant", TurnIndex: &turnIdx0,
				PartsJSON: []byte(`[{"type":"text","text":"hi"},{"type":"dynamic-tool","toolName":"Read","toolCallId":"t1"}]`),
				SourceLine: 3, OccurredAt: &turn0End},
			{Sequence: 5, ProviderMessageID: "uuid-5", Role: "user", TurnIndex: &turnIdx1,
				PartsJSON: []byte(`[{"type":"text","text":"next"}]`), SourceLine: 5},
			{Sequence: 7, ProviderMessageID: "uuid-7", Role: "assistant", TurnIndex: &turnIdx1,
				PartsJSON: []byte(`[{"type":"text","text":"done"}]`), SourceLine: 7, OccurredAt: &modTime},
		},
	}
}

func TestIngestTranscriptAndReadStores(t *testing.T) {
	db := openIngestTestDB(t)
	modTime := time.Now().UTC().Truncate(time.Second)

	session, err := db.IngestTranscript(t.Context(), testIngestBatch(modTime, 4096))
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, session.ID)

	t.Run("overview aggregates from ingested turns and messages", func(t *testing.T) {
		overview, err := db.GetSessionOverviewByIdentity(t.Context(), testProviderSessionID)
		require.NoError(t, err)
		assert.Equal(t, session.ID, overview.ID)
		assert.EqualValues(t, 4, overview.MessageCount)
		assert.EqualValues(t, 1, overview.ToolCallCount)
		assert.EqualValues(t, 2, overview.TurnCount)
		assert.EqualValues(t, 3000, overview.InputTokens)
		assert.EqualValues(t, 600, overview.OutputTokens)
		assert.EqualValues(t, 13000, overview.CacheReadTokens)
		assert.InDelta(t, 0.018, overview.CostUSD, 1e-9)
		require.NotNil(t, overview.ContextFreePercent)
		assert.Equal(t, 70, *overview.ContextFreePercent, "latest call: 1-60000/200000 = 70%")
		require.NotNil(t, overview.Model)
		assert.Equal(t, "claude-sonnet-5", *overview.Model)
		require.NotNil(t, overview.HistoryFile)
		assert.Equal(t, testTranscriptPath, *overview.HistoryFile)
		assert.False(t, overview.ProcessActive)
	})

	t.Run("prefix identity resolves and full re-ingest is idempotent", func(t *testing.T) {
		byPrefix, err := db.GetSessionOverviewByIdentity(t.Context(), testProviderSessionID[:8])
		require.NoError(t, err)
		assert.Equal(t, session.ID, byPrefix.ID)

		replayed, err := db.IngestTranscript(t.Context(), testIngestBatch(modTime, 4096))
		require.NoError(t, err)
		assert.Equal(t, session.ID, replayed.ID)
		overview, err := db.GetSessionOverviewByIdentity(t.Context(), testProviderSessionID)
		require.NoError(t, err)
		assert.EqualValues(t, 4, overview.MessageCount, "replay must not duplicate messages")
		assert.EqualValues(t, 2, overview.TurnCount, "replay must not duplicate turns")
		assert.EqualValues(t, 3000, overview.InputTokens, "replay must not duplicate model calls")
	})

	t.Run("session sources bookkeeping is listed by path", func(t *testing.T) {
		sources, err := db.ListSessionSources(t.Context())
		require.NoError(t, err)
		state, ok := sources[testTranscriptPath]
		require.True(t, ok)
		assert.Equal(t, session.ID, state.SessionID)
		assert.EqualValues(t, 4096, state.ByteOffset)
		assert.Equal(t, testParserVersion, state.ParserVersion)
		require.NotNil(t, state.ObservedModTime)
		assert.WithinDuration(t, modTime, *state.ObservedModTime, time.Second)
	})

	t.Run("extended batch converges the final turn and appends messages", func(t *testing.T) {
		later := modTime.Add(2 * time.Minute)
		extended := testIngestBatch(later, 8192)
		extended.Turns[1].EndedAt = &later
		extended.Turns[1].Call.InputTokens = 2500
		extended.Turns[1].Call.OutputTokens = 700
		turnIdx1 := 1
		extended.Messages = append(extended.Messages, IngestMessage{
			Sequence: 9, ProviderMessageID: "uuid-9", Role: "assistant", TurnIndex: &turnIdx1,
			PartsJSON: []byte(`[{"type":"text","text":"appended"}]`), SourceLine: 9, OccurredAt: &later,
		})
		_, err := db.IngestTranscript(t.Context(), extended)
		require.NoError(t, err)

		overview, err := db.GetSessionOverviewByIdentity(t.Context(), testProviderSessionID)
		require.NoError(t, err)
		assert.EqualValues(t, 5, overview.MessageCount)
		assert.EqualValues(t, 2, overview.TurnCount)
		assert.EqualValues(t, 3500, overview.InputTokens, "final turn call converges in place")
		assert.EqualValues(t, 900, overview.OutputTokens)

		sources, err := db.ListSessionSources(t.Context())
		require.NoError(t, err)
		assert.EqualValues(t, 8192, sources[testTranscriptPath].ByteOffset)
	})

	t.Run("transcript paging by offset, limit, and tail with source lines", func(t *testing.T) {
		page, err := db.ListTranscriptMessages(t.Context(), TranscriptPage{SessionID: session.ID, Offset: 1, Limit: 2})
		require.NoError(t, err)
		require.Len(t, page, 2)
		assert.EqualValues(t, 3, page[0].Sequence)
		assert.EqualValues(t, 5, page[1].Sequence)
		require.NotNil(t, page[0].SourceLine)
		assert.EqualValues(t, 3, *page[0].SourceLine)

		tail, err := db.ListTranscriptMessages(t.Context(), TranscriptPage{SessionID: session.ID, Tail: 2})
		require.NoError(t, err)
		require.Len(t, tail, 2)
		assert.EqualValues(t, 7, tail[0].Sequence)
		assert.EqualValues(t, 9, tail[1].Sequence)
	})

	t.Run("agent transcript ingests as child session", func(t *testing.T) {
		agentPath := filepath.Join(filepath.Dir(testTranscriptPath), "subagents", "agent-1.jsonl")
		child := IngestTranscriptInput{
			Session: IngestSessionInput{
				ProviderSessionID: "agent-1", Source: "claude", HostID: "test-host",
				ParentSessionID: &session.ID, Path: agentPath, AgentType: "Explore",
				Description: "search the codebase",
			},
			Source: IngestSourceInput{
				SourceKind: "claude", Path: agentPath, ParserVersion: testParserVersion,
				ByteOffset: 100, ObservedSize: 100, ObservedModTime: modTime,
			},
			Messages: []IngestMessage{
				{Sequence: 1, Role: "user", PartsJSON: []byte(`[{"type":"text","text":"task"}]`), SourceLine: 1},
			},
		}
		childSession, err := db.IngestTranscript(t.Context(), child)
		require.NoError(t, err)
		require.NotNil(t, childSession.ParentSessionID)
		assert.Equal(t, session.ID, *childSession.ParentSessionID)

		overview, err := db.GetSessionOverviewByIdentity(t.Context(), testProviderSessionID)
		require.NoError(t, err)
		assert.EqualValues(t, 2, overview.AgentCount)

		roots, err := db.ListSessionOverviews(t.Context(), SessionOverviewFilter{RootsOnly: true})
		require.NoError(t, err)
		require.Len(t, roots, 1)
		assert.Equal(t, session.ID, roots[0].ID)
	})

	t.Run("process lifecycle: upsert refreshes metrics, vanish closes", func(t *testing.T) {
		processStart := modTime.Add(-5 * time.Minute)
		input := SessionProcessInput{
			SessionID: session.ID, HostID: "test-host", BootID: "boot-1", PID: 4242,
			ProcessStartedAt: processStart, Status: "running", Command: "claude --resume",
			CWD: "/home/dev/example", Source: "claude", CPUPercent: 12.5, MemoryPercent: 3.25,
			SampledAt: modTime,
		}
		require.NoError(t, db.UpsertSessionProcess(t.Context(), input))
		input.CPUPercent = 55.75
		require.NoError(t, db.UpsertSessionProcess(t.Context(), input))

		overview, err := db.GetSessionOverviewByIdentity(t.Context(), testProviderSessionID)
		require.NoError(t, err)
		assert.True(t, overview.ProcessActive)
		require.NotNil(t, overview.PID)
		assert.EqualValues(t, 4242, *overview.PID)
		require.NotNil(t, overview.CPUPercent)
		assert.InDelta(t, 55.75, *overview.CPUPercent, 1e-9)

		live, err := db.ListSessionOverviews(t.Context(), SessionOverviewFilter{LiveOnly: true})
		require.NoError(t, err)
		require.Len(t, live, 1)

		closed, err := db.EndVanishedProcesses(t.Context(), "test-host", []int64{9999})
		require.NoError(t, err)
		assert.EqualValues(t, 1, closed)
		overview, err = db.GetSessionOverviewByIdentity(t.Context(), testProviderSessionID)
		require.NoError(t, err)
		assert.False(t, overview.ProcessActive)
	})

	t.Run("project aggregates group sessions and live processes", func(t *testing.T) {
		projects, err := db.ListProjectAggregates(t.Context())
		require.NoError(t, err)
		require.Len(t, projects, 1)
		assert.Equal(t, "example", projects[0].Project)
		assert.EqualValues(t, 1, projects[0].SessionCount)
		assert.EqualValues(t, 0, projects[0].LiveCount)
		assert.Equal(t, "claude", projects[0].Sources)
	})
}
