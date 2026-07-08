package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai/history"
)

func TestBuildCodexSession_MapsMessagesFilesAndMeta(t *testing.T) {
	ts := time.Date(2026, 7, 6, 10, 0, 0, 0, time.UTC)
	uses := []history.ToolUse{
		{Tool: "Reasoning", Input: map[string]any{"text": "thinking about it"}, Timestamp: &ts, SessionID: "cx-1", Source: "codex"},
		{Tool: "Assistant", Input: map[string]any{"text": "here is the answer"}, Timestamp: &ts, SessionID: "cx-1", Source: "codex", Model: "gpt-5"},
		{Tool: "Write", Input: map[string]any{"file_path": "/repo/a.go"}, ToolUseID: "t1", Response: "wrote 1 file", Timestamp: &ts, SessionID: "cx-1", Source: "codex"},
		{Tool: "Read", Input: map[string]any{"file_path": "/repo/b.go"}, ToolUseID: "t2", Timestamp: &ts, SessionID: "cx-1", Source: "codex"},
	}
	info := &history.CodexSessionInfo{ID: "cx-1", CWD: "/repo", ModelProvider: "openai", CLIVersion: "0.1.0", GitBranch: "main", GitCommit: "abc123", Model: "gpt-5"}

	s := buildCodexSession(uses, info)

	if s.Source != "codex" || s.ID != "cx-1" || s.Model != "gpt-5" {
		t.Fatalf("session meta = %+v", s)
	}
	if s.Provider != "openai" || s.Git.Branch != "main" || s.Git.Commit != "abc123" {
		t.Errorf("provider/git = %q %+v", s.Provider, s.Git)
	}
	if len(s.Messages) != 4 {
		t.Fatalf("messages = %d, want 4", len(s.Messages))
	}
	if p := s.Messages[0].Parts[0]; p.Type != PartReasoning || p.Text != "thinking about it" {
		t.Errorf("msg0 = %+v, want reasoning", p)
	}
	if p := s.Messages[1].Parts[0]; p.Type != PartText || p.Text != "here is the answer" {
		t.Errorf("msg1 = %+v, want text", p)
	}
	write := s.Messages[2].Parts[0]
	if write.Type != PartTool || write.ToolName != "Write" || write.State != ToolStateOutputAvailable {
		t.Errorf("msg2 = %+v, want Write tool with output", write)
	}
	if string(write.Output) != `"wrote 1 file"` {
		t.Errorf("write output = %s, want the inline response as JSON", write.Output)
	}
	// Paths are relativized to the session CWD (/repo), matching the claude model.
	if want := []string{"a.go"}; !equalStrings(s.Files.Written, want) {
		t.Errorf("written = %v, want %v", s.Files.Written, want)
	}
	if want := []string{"b.go"}; !equalStrings(s.Files.Read, want) {
		t.Errorf("read = %v, want %v", s.Files.Read, want)
	}
	// Codex carries no token usage → zero cost.
	if s.Cost.Total() != 0 {
		t.Errorf("codex cost = %v, want 0", s.Cost.Total())
	}
}

func TestBuildCodexSession_AttachesLatestInlinePlan(t *testing.T) {
	ts := time.Date(2026, 7, 6, 10, 0, 0, 0, time.UTC)
	uses := []history.ToolUse{
		{Tool: "TodoWrite", Input: map[string]any{"todos": []any{
			map[string]any{"step": "old step", "status": "in_progress"},
		}}, Timestamp: &ts, SessionID: "cx-plan", Source: "codex"},
		{Tool: "TodoWrite", Input: map[string]any{"todos": []any{
			map[string]any{"step": "inspect code", "status": "completed"},
			map[string]any{"content": "run tests", "status": "in_progress"},
		}}, Timestamp: &ts, SessionID: "cx-plan", Source: "codex"},
	}

	s := buildCodexSession(uses, &history.CodexSessionInfo{ID: "cx-plan", CWD: "/repo"})

	if s.Plan == nil {
		t.Fatal("plan is nil")
	}
	if got, want := s.Plan.Content, "- [x] inspect code\n- [ ] run tests _(in progress)_"; got != want {
		t.Fatalf("plan content = %q, want %q", got, want)
	}
	if !s.Plan.Explicit || len(s.Plan.Events) != 1 || s.Plan.Events[0].Kind != PlanWrite {
		t.Fatalf("plan metadata = %+v", s.Plan)
	}
}

func TestBuildCodexSession_MapsUserMessagesAndEvents(t *testing.T) {
	stream := strings.Join([]string{
		`{"timestamp":"2026-07-08T11:19:57.028Z","type":"session_meta","payload":{"id":"sess-rollout","cwd":"/repo","cli_version":"0.143.0","model_provider":"openai"}}`,
		`{"timestamp":"2026-07-08T11:19:57.028Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2026-07-08T11:19:58.758Z","type":"turn_context","payload":{"model":"gpt-5.5","effort":"high"}}`,
		`{"timestamp":"2026-07-08T11:19:58.760Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}`,
		`{"timestamp":"2026-07-08T11:19:58.760Z","type":"event_msg","payload":{"type":"user_message","message":"hi"}}`,
		`{"timestamp":"2026-07-08T11:20:00.403Z","type":"event_msg","payload":{"type":"agent_message","message":"hello"}}`,
		`{"timestamp":"2026-07-08T11:20:00.403Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}}`,
		`{"timestamp":"2026-07-08T11:20:00.435Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","duration_ms":3519}}`,
	}, "\n")
	uses, err := history.ExtractCodexToolUsesFromReader(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("ExtractCodexToolUsesFromReader: %v", err)
	}

	s := buildCodexSession(uses, &history.CodexSessionInfo{ID: "sess-rollout", CWD: "/repo", Model: "gpt-5.5"})

	if len(s.Messages) != 2 {
		t.Fatalf("messages = %d, want 2: %+v", len(s.Messages), s.Messages)
	}
	if s.Messages[0].Role != "user" || s.Messages[0].Parts[0].Text != "hi" {
		t.Fatalf("first message = %+v, want user hi", s.Messages[0])
	}
	if s.Messages[1].Role != "assistant" || s.Messages[1].Parts[0].Text != "hello" {
		t.Fatalf("second message = %+v, want assistant hello", s.Messages[1])
	}
	if len(s.Events) != 2 || s.Events[0].Type != "task_started" || s.Events[1].Type != "task_complete" {
		t.Fatalf("events = %+v", s.Events)
	}
}

func TestBuildCodex_AttachesHistoryFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "rollout-2026-07-08T14-19-56-sess-rollout.jsonl")
	stream := strings.Join([]string{
		`{"timestamp":"2026-07-08T11:19:57.028Z","type":"session_meta","payload":{"id":"sess-rollout","cwd":"/repo","cli_version":"0.143.0","model_provider":"openai"}}`,
		`{"timestamp":"2026-07-08T11:19:58.760Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}`,
	}, "\n")
	if err := os.WriteFile(file, []byte(stream), 0o600); err != nil {
		t.Fatalf("write codex fixture: %v", err)
	}

	sessions := BuildCodex([]string{file})
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}
	if got := sessions[0].HistoryFile; got != file {
		t.Fatalf("history file = %q, want %q", got, file)
	}
	if sessions[0].Root == nil || sessions[0].Root.HistoryFile != file {
		t.Fatalf("root history file = %+v, want %q", sessions[0].Root, file)
	}
}
