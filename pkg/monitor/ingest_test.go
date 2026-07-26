package monitor

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
)

func TestUnifiedIngestInputPreservesTurnEffort(t *testing.T) {
	input := unifiedIngestInput(&session.Session{Turns: []session.Turn{{
		ID: "turn-1", Index: 1, Model: "gpt-5.6-sol", ReasoningEffort: "max",
	}}}, "codex", func(session.Message, int) int64 { return 0 })

	if len(input.Turns) != 1 || input.Turns[0].Call == nil {
		t.Fatalf("turns = %+v, want one model call", input.Turns)
	}
	if input.Turns[0].Call.Effort != "max" {
		t.Fatalf("effort = %q, want max", input.Turns[0].Call.Effort)
	}
}

func TestUnifiedIngestInputPreservesAgentTurnOwnership(t *testing.T) {
	input := unifiedIngestInput(&session.Session{Turns: []session.Turn{{
		ID: "child/turn-1", AgentID: "child", Index: 1, Model: "claude-haiku-4-5",
	}}}, "claude", func(session.Message, int) int64 { return 0 })

	if len(input.Turns) != 1 || input.Turns[0].ProviderTurnID != "child/turn-1" {
		t.Fatalf("turns = %+v, want the child agent turn", input.Turns)
	}
}

func messagesAt(sequences ...int64) []database.IngestMessage {
	messages := make([]database.IngestMessage, 0, len(sequences))
	for _, sequence := range sequences {
		messages = append(messages, database.IngestMessage{Sequence: sequence})
	}
	return messages
}

func sequencesOf(messages []database.IngestMessage) []int64 {
	sequences := make([]int64, 0, len(messages))
	for _, message := range messages {
		sequences = append(sequences, message.Sequence)
	}
	return sequences
}

func TestHighWaterMarkWritesOnlyAppendedMessages(t *testing.T) {
	for _, test := range []struct {
		name     string
		previous int64
		parsed   []int64
		want     []int64
		wantMark int64
	}{
		{name: "first ingest writes the whole file", parsed: []int64{1, 3, 5}, want: []int64{1, 3, 5}, wantMark: 5},
		{name: "an append writes only the appended lines", previous: 3, parsed: []int64{1, 3, 5, 7}, want: []int64{5, 7}, wantMark: 7},
		{name: "a re-parse with nothing new writes nothing", previous: 5, parsed: []int64{1, 3, 5}, wantMark: 5},
		// A non-conversational line (a hook record, a summary) grows the file
		// without producing a message, so the mark must survive an empty pass.
		{name: "growth with no new messages keeps the mark", previous: 5, wantMark: 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := database.IngestTranscriptInput{Messages: messagesAt(test.parsed...)}
			mark := highWaterMark(&input, test.previous)

			if mark != test.wantMark {
				t.Fatalf("mark = %d, want %d", mark, test.wantMark)
			}
			got := sequencesOf(input.Messages)
			if len(got) != len(test.want) {
				t.Fatalf("wrote sequences %v, want %v", got, test.want)
			}
			for i, sequence := range test.want {
				if got[i] != sequence {
					t.Fatalf("wrote sequences %v, want %v", got, test.want)
				}
			}
		})
	}
}

// The mtime a pass records has to compare equal to the mtime the next pass
// reads, or the skip is dead code. It goes through a microsecond column, so the
// nanoseconds APFS reports must be dropped before the value is ever stored.
func TestNeedsIngestSkipsAFileWhoseModTimeSurvivedTheDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, make([]byte, 2048), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if observedModTime(info).Nanosecond()%1000 != 0 {
		t.Fatalf("observedModTime kept sub-microsecond precision a timestamptz column cannot store: %v",
			observedModTime(info))
	}

	// What ListSessionSources hands back: the recorded value after Postgres has
	// stored it at microsecond resolution.
	stored := observedModTime(info).Truncate(time.Microsecond)
	ing := &ingestor{sources: map[string]database.SessionSourceState{path: {
		ParserVersion: parserVersion, ObservedSize: info.Size(), ObservedModTime: &stored,
	}}}
	if ing.needsIngest(path, info) {
		t.Fatal("an unchanged file must be skipped, not re-ingested in full")
	}

	if err := os.WriteFile(path, make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	grown, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ing.needsIngest(path, grown) {
		t.Fatal("a file that changed on disk must be re-ingested")
	}
}

func TestResumeAfterVoidsUntrustworthyMarks(t *testing.T) {
	const size = 2048
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name  string
		state *database.SessionSourceState
		want  int64
	}{
		{name: "an unseen file replays in full", want: 0},
		{
			name: "an appended file resumes from its mark",
			state: &database.SessionSourceState{
				ParserVersion: parserVersion, ObservedSize: size - 128, LastEventKey: "42",
			},
			want: 42,
		},
		{
			name: "a parser bump replays in full",
			state: &database.SessionSourceState{
				ParserVersion: parserVersion - 1, ObservedSize: size - 128, LastEventKey: "42",
			},
			want: 0,
		},
		{
			name: "a shrunken file was rewritten, not appended to",
			state: &database.SessionSourceState{
				ParserVersion: parserVersion, ObservedSize: size + 128, LastEventKey: "42",
			},
			want: 0,
		},
		{
			name: "rows written before the mark existed replay in full",
			state: &database.SessionSourceState{
				ParserVersion: parserVersion, ObservedSize: size - 128, LastEventKey: "",
			},
			want: 0,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ing := &ingestor{sources: map[string]database.SessionSourceState{}}
			if test.state != nil {
				ing.sources[path] = *test.state
			}
			if got := ing.resumeAfter(path, info); got != test.want {
				t.Fatalf("resumeAfter = %d, want %d", got, test.want)
			}
		})
	}
}
