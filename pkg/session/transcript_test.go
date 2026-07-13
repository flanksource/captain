package session

import (
	"os"
	"path/filepath"
	"testing"
)

const transcriptFixture = `{"type":"user","uuid":"u-1","sessionId":"0195c1de-4ab8-7000-8000-0123456789ab","timestamp":"2026-07-05T10:00:00Z","cwd":"/home/dev/example","message":{"role":"user","content":"hello"}}
{"type":"system","subtype":"init","uuid":"sys-1","sessionId":"0195c1de-4ab8-7000-8000-0123456789ab","timestamp":"2026-07-05T10:00:01Z","cwd":"/home/dev/example","model":"claude-sonnet-5"}
{"type":"assistant","uuid":"a-1","sessionId":"0195c1de-4ab8-7000-8000-0123456789ab","timestamp":"2026-07-05T10:00:05Z","message":{"role":"assistant","model":"claude-sonnet-5","usage":{"input_tokens":100,"output_tokens":20},"content":[{"type":"text","text":"hi there"}]}}
{"type":"assistant","uuid":"a-2","sessionId":"0195c1de-4ab8-7000-8000-0123456789ab","timestamp":"2026-07-05T10:00:08Z","message":{"role":"assistant","model":"claude-sonnet-5","usage":{"input_tokens":150,"output_tokens":30},"content":[{"type":"tool_use","id":"t-1","name":"Read","input":{"file_path":"main.go"}}]}}
`

func writeTranscriptFixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "projects", "-home-dev-example")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "0195c1de-4ab8-7000-8000-0123456789ab.jsonl")
	if err := os.WriteFile(path, []byte(transcriptFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestBuildTranscriptFile_SourceLines verifies each message carries the 1-based
// JSONL line it came from, so consumers can seek back into the transcript, and
// that the synthetic system/init line is classified as non-conversational.
func TestBuildTranscriptFile_SourceLines(t *testing.T) {
	path := writeTranscriptFixture(t)
	s, info, err := BuildTranscriptFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.RootSessionID != "0195c1de-4ab8-7000-8000-0123456789ab" {
		t.Fatalf("root session id = %q", info.RootSessionID)
	}
	if info.IsAgent {
		t.Fatal("root transcript misclassified as agent")
	}

	linesByUUID := map[string]int64{}
	conversational := 0
	for _, m := range s.Messages {
		linesByUUID[m.ID] = m.SourceLine
		if IsConversationalMessage(m) {
			conversational++
		}
	}
	expected := map[string]int64{"u-1": 1, "a-1": 3, "a-2": 4}
	for id, line := range expected {
		if linesByUUID[id] != line {
			t.Errorf("message %s source line = %d, want %d", id, linesByUUID[id], line)
		}
	}
	if conversational != 3 {
		t.Errorf("conversational messages = %d, want 3 (system/init must be filtered)", conversational)
	}
}

func TestIsSyntheticEventTool(t *testing.T) {
	for _, name := range []string{"SessionInit", "HookStart", "Result", "ApiError", "ParseError", "TurnDuration"} {
		if !IsSyntheticEventTool(name) {
			t.Errorf("%s should be synthetic", name)
		}
	}
	for _, name := range []string{"Read", "Bash", "ExitPlanMode", "Plan", ""} {
		if IsSyntheticEventTool(name) {
			t.Errorf("%s should not be synthetic", name)
		}
	}
}
