package session

import (
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
