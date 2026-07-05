package session

import (
	"math"
	"testing"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/segmentio/encoding/json"
)

func rawInput(t *testing.T, v map[string]any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	return b
}

func assistantEntry(uuid, parentUUID, model string, usage *claude.Usage, blocks ...claude.ContentBlock) claude.HistoryEntry {
	return claude.HistoryEntry{
		UUID:       uuid,
		ParentUUID: parentUUID,
		SessionID:  "root-sess",
		Timestamp:  "2026-07-05T10:00:00Z",
		Message: claude.Message{
			Role:    claude.MessageRoleAssistant,
			Model:   model,
			Usage:   usage,
			Content: blocks,
		},
	}
}

func toolUseBlock(id, name string, input json.RawMessage) claude.ContentBlock {
	return claude.ContentBlock{Type: claude.ContentTypeToolUse, ID: id, Name: name, Input: input}
}

// TestBuildHierarchy_MultiLevelParentLinkage verifies grandchild agents are
// attributed to their true parent agent (via parentUuid), not flattened to the
// root session — the F6 fix.
func TestBuildHierarchy_MultiLevelParentLinkage(t *testing.T) {
	root := claude.ParsedTranscript{
		Path:    "/p/root-sess.jsonl",
		Entries: []claude.HistoryEntry{{UUID: "root-1", SessionID: "root-sess", Timestamp: "2026-07-05T10:00:00Z", Message: claude.Message{Role: claude.MessageRoleAssistant}}},
	}
	child := claude.ParsedTranscript{
		Path: "/p/root-sess/subagents/agent-child.jsonl", IsAgent: true, AgentID: "child", AgentType: "explorer",
		Entries: []claude.HistoryEntry{{UUID: "child-1", ParentUUID: "root-1", Timestamp: "2026-07-05T10:01:00Z", Message: claude.Message{Role: claude.MessageRoleAssistant}}},
	}
	grandchild := claude.ParsedTranscript{
		Path: "/p/root-sess/subagents/agent-grand.jsonl", IsAgent: true, AgentID: "grand", AgentType: "worker",
		Entries: []claude.HistoryEntry{{UUID: "grand-1", ParentUUID: "child-1", Timestamp: "2026-07-05T10:02:00Z", Message: claude.Message{Role: claude.MessageRoleAssistant}}},
	}

	h := buildHierarchy(claude.ParsedSession{SessionID: "root-sess", Transcripts: []claude.ParsedTranscript{root, child, grandchild}})

	byID := map[string]*Agent{}
	for _, a := range h.agents {
		byID[a.ID] = a
	}
	if got := byID["child"].ParentID; got != "root-sess" {
		t.Errorf("child parent = %q, want root-sess", got)
	}
	if got := byID["grand"].ParentID; got != "child" {
		t.Errorf("grandchild parent = %q, want child (flattened to root would be the bug)", got)
	}
	if n := len(byID["root-sess"].Children); n != 1 || byID["root-sess"].Children[0].ID != "child" {
		t.Errorf("root children = %v, want [child]", byID["root-sess"].Children)
	}
	if n := len(byID["child"].Children); n != 1 || byID["child"].Children[0].ID != "grand" {
		t.Errorf("child children = %v, want [grand]", byID["child"].Children)
	}
}

func TestChangedFiles_ReadWriteSplit(t *testing.T) {
	uses := []claude.ToolUse{
		{Tool: "Read", Input: map[string]any{"file_path": "/repo/a.go"}, ProjectRoot: "/repo"},
		{Tool: "Write", Input: map[string]any{"file_path": "/repo/b.go"}, ProjectRoot: "/repo"},
		{Tool: "Edit", Input: map[string]any{"file_path": "/repo/b.go"}, ProjectRoot: "/repo"},
		{Tool: "Grep", Input: map[string]any{"path": "/repo/pkg"}, ProjectRoot: "/repo"},
	}
	cf := changedFiles(uses)
	if want := []string{"a.go", "pkg"}; !equalStrings(cf.Read, want) {
		t.Errorf("read = %v, want %v", cf.Read, want)
	}
	if want := []string{"b.go"}; !equalStrings(cf.Written, want) { // deduped across Write+Edit
		t.Errorf("written = %v, want %v", cf.Written, want)
	}
}

func TestApprovalStats(t *testing.T) {
	uses := []claude.ToolUse{
		{Tool: "Write", ToolUseID: "1"},
		{Tool: "Bash", ToolUseID: "2", Denied: true, DeniedReason: "no rm -rf"},
		{Tool: "ExitPlanMode", ToolUseID: "3"},
	}
	got := approvalStats(uses)
	if got.Approved != 1 {
		t.Errorf("approved = %d, want 1", got.Approved)
	}
	if got.Denied != 1 || len(got.Denials) != 1 || got.Denials[0].Reason != "no rm -rf" {
		t.Errorf("denials = %+v, want one with reason 'no rm -rf'", got.Denials)
	}
}

func TestCostFromUsage_OpusPricing(t *testing.T) {
	c := CostFromUsage(&claude.Usage{InputTokens: 1000, OutputTokens: 500}, "claude-opus-4")
	// opus: input 15/Mtok, output 75/Mtok → 0.015 + 0.0375
	if want := 0.0525; math.Abs(c.Total()-want) > 1e-9 {
		t.Errorf("total = %v, want %v", c.Total(), want)
	}
	if c.InputTokens != 1000 || c.OutputTokens != 500 {
		t.Errorf("tokens = %d/%d, want 1000/500", c.InputTokens, c.OutputTokens)
	}
}

func TestBuildSession_CostFilesPlanApprovals(t *testing.T) {
	writeBlock := toolUseBlock("w1", "Write", rawInput(t, map[string]any{"file_path": "/repo/x.go", "content": "package x"}))
	planBlock := toolUseBlock("p1", "ExitPlanMode", rawInput(t, map[string]any{"planFilePath": "/home/u/.claude/plans/foo.md", "plan": "do X"}))
	e := assistantEntry("a1", "", "claude-opus-4",
		&claude.Usage{InputTokens: 1000, OutputTokens: 500},
		claude.ContentBlock{Type: claude.ContentTypeText, Text: "hello"},
		writeBlock, planBlock,
	)
	// stamp ProjectRoot so changed-files relativizes.
	uses := claude.ExtractToolUsesWithTokens([]claude.HistoryEntry{e})
	for i := range uses {
		uses[i].ProjectRoot = "/repo"
	}
	ps := claude.ParsedSession{
		SessionID:   "root-sess",
		Transcripts: []claude.ParsedTranscript{{Path: "/p/root-sess.jsonl", Entries: []claude.HistoryEntry{e}, ToolUses: uses}},
	}

	s := buildSession(ps)

	if want := 0.0525; math.Abs(s.Cost.Total()-want) > 1e-9 {
		t.Errorf("session cost = %v, want %v", s.Cost.Total(), want)
	}
	if s.Usage.InputTokens != 1000 {
		t.Errorf("usage input = %d, want 1000", s.Usage.InputTokens)
	}
	if want := []string{"x.go"}; !equalStrings(s.Files.Written, want) {
		t.Errorf("written = %v, want %v", s.Files.Written, want)
	}
	if s.Plan == nil || !s.Plan.Explicit || s.Plan.Path != "/home/u/.claude/plans/foo.md" {
		t.Errorf("plan = %+v, want explicit foo.md", s.Plan)
	}
	if len(s.Messages) == 0 || s.Messages[0].Role != "assistant" {
		t.Errorf("messages = %+v, want a leading assistant message", s.Messages)
	}
	// Write + ExitPlanMode both counted; ExitPlanMode excluded from approvals.
	if s.Approvals.Approved != 1 {
		t.Errorf("approved = %d, want 1 (Write only)", s.Approvals.Approved)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
