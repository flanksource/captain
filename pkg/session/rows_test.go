package session

import (
	"testing"

	"github.com/flanksource/captain/pkg/claude"
)

// TestRows_RootAndSubAgentLinked verifies each transcript becomes its own Row and
// a sub-agent Row is linked to its parent, carrying per-transcript metadata.
func TestRows_RootAndSubAgentLinked(t *testing.T) {
	writeInput := rawInput(t, map[string]any{"file_path": "/repo/x.go", "content": "x"})
	rootEntry := claude.HistoryEntry{
		UUID: "root-1", SessionID: "sess", Timestamp: "2026-07-06T10:00:00Z", CWD: "/repo", GitBranch: "main",
		Message: claude.Message{
			Role: claude.MessageRoleAssistant, Model: "claude-opus-4",
			Usage:   &claude.Usage{InputTokens: 1000, OutputTokens: 500},
			Content: []claude.ContentBlock{{Type: claude.ContentTypeText, Text: "spawning"}},
		},
	}
	childEntry := claude.HistoryEntry{
		UUID: "child-1", ParentUUID: "root-1", Timestamp: "2026-07-06T10:01:00Z", CWD: "/repo",
		Message: claude.Message{
			Role: claude.MessageRoleAssistant, Model: "claude-opus-4",
			Content: []claude.ContentBlock{toolUseBlock("t1", "Write", writeInput)},
		},
	}
	childUses := claude.ExtractToolUsesWithTokens([]claude.HistoryEntry{childEntry})
	for i := range childUses {
		childUses[i].ProjectRoot = "/repo"
	}

	ps := claude.ParsedSession{
		SessionID: "sess",
		Transcripts: []claude.ParsedTranscript{
			{Path: "/p/sess.jsonl", Entries: []claude.HistoryEntry{rootEntry}},
			{Path: "/p/sess/subagents/agent-c.jsonl", IsAgent: true, AgentID: "c", AgentType: "worker", AgentDesc: "do it",
				Entries: []claude.HistoryEntry{childEntry}, ToolUses: childUses},
		},
	}

	rows := Rows(ps)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	byID := map[string]Row{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	root := byID["sess"]
	if root.IsAgent || root.ParentID != "" || root.Path != "/p/sess.jsonl" || root.Git.Branch != "main" {
		t.Errorf("root row = %+v", root)
	}
	if root.Cost.Total() == 0 {
		t.Errorf("root cost should be computed from usage, got 0")
	}
	child := byID["c"]
	if !child.IsAgent || child.ParentID != "sess" || child.AgentType != "worker" {
		t.Errorf("child row = %+v, want agent linked to sess", child)
	}
	if want := []string{"x.go"}; !equalStrings(child.Files.Written, want) {
		t.Errorf("child written = %v, want %v", child.Files.Written, want)
	}
}
