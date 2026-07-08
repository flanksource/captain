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

	h := buildHierarchy(claude.ParsedSession{SessionID: "root-sess", Transcripts: []claude.ParsedTranscript{root, child, grandchild}}, nil)

	byID := map[string]*Agent{}
	for _, a := range h.agents {
		byID[a.ID] = a
	}
	if got := byID["child"].ParentID; got != "root-sess" {
		t.Errorf("child parent = %q, want root-sess", got)
	}
	if got := byID["root-sess"].HistoryFile; got != "/p/root-sess.jsonl" {
		t.Errorf("root history file = %q, want /p/root-sess.jsonl", got)
	}
	if got := byID["child"].HistoryFile; got != "/p/root-sess/subagents/agent-child.jsonl" {
		t.Errorf("child history file = %q, want agent transcript path", got)
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

	if got := s.HistoryFile; got != "/p/root-sess.jsonl" {
		t.Errorf("history file = %q, want /p/root-sess.jsonl", got)
	}
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

func TestBuildSession_MetadataTurnsCapabilitiesBudget(t *testing.T) {
	entries := []claude.HistoryEntry{
		{
			UUID:      "tools-1",
			SessionID: "root-sess",
			Timestamp: "2026-07-05T10:00:00Z",
			Event: &claude.TranscriptEvent{
				Type:  "deferred_tools_delta",
				Scope: "session",
				Data: map[string]any{
					"addedNames":        []any{"Read", "Bash"},
					"pendingMcpServers": []any{"github"},
				},
			},
		},
		{
			UUID:      "agents-1",
			SessionID: "root-sess",
			Timestamp: "2026-07-05T10:00:01Z",
			Event: &claude.TranscriptEvent{
				Type:  "agent_listing_delta",
				Scope: "session",
				Data:  map[string]any{"addedTypes": []any{"general-purpose"}},
			},
		},
		{
			UUID:      "skills-1",
			SessionID: "root-sess",
			Timestamp: "2026-07-05T10:00:02Z",
			Event: &claude.TranscriptEvent{
				Type:  "skill_listing",
				Scope: "session",
				Data:  map[string]any{"content": "- gavel-runner: Run gavel tests\n- iconography: Pick icons"},
			},
		},
		{
			UUID:      "queue-1",
			SessionID: "root-sess",
			Timestamp: "2026-07-05T10:00:03Z",
			Event: &claude.TranscriptEvent{
				Type:  "queue-operation",
				Scope: "turn",
				Data:  map[string]any{"operation": "enqueue"},
			},
		},
		{
			UUID:      "budget-1",
			SessionID: "root-sess",
			Timestamp: "2026-07-05T10:00:04Z",
			Event: &claude.TranscriptEvent{
				Type:  "budget_usd",
				Scope: "turn",
				Data:  map[string]any{"used": 1.25, "total": 5.0, "remaining": 3.75},
			},
		},
		{
			UUID:      "user-1",
			SessionID: "root-sess",
			Timestamp: "2026-07-05T10:00:05Z",
			Message: claude.Message{
				Role:    claude.MessageRoleUser,
				Content: []claude.ContentBlock{{Type: claude.ContentTypeText, Text: "fix it"}},
			},
		},
		{
			UUID:      "assistant-1",
			SessionID: "root-sess",
			Timestamp: "2026-07-05T10:00:06Z",
			Message: claude.Message{
				Role:       claude.MessageRoleAssistant,
				Model:      "claude-opus-4",
				StopReason: claude.StopReasonEndTurn,
				Usage: &claude.Usage{
					InputTokens:              1000,
					OutputTokens:             500,
					CacheCreationInputTokens: 200,
					CacheReadInputTokens:     300,
				},
				Content: []claude.ContentBlock{{Type: claude.ContentTypeText, Text: "done"}},
			},
		},
		{
			UUID:      "prompt-1",
			SessionID: "root-sess",
			Timestamp: "2026-07-05T10:00:07Z",
			Event: &claude.TranscriptEvent{
				Type:  "last-prompt",
				Scope: "session",
				Data:  map[string]any{"content": "fix it"},
			},
		},
	}

	s := buildSession(claude.ParsedSession{
		SessionID:   "root-sess",
		Transcripts: []claude.ParsedTranscript{{Path: "/p/root-sess.jsonl", Entries: entries}},
	})

	if !equalStrings(s.Capabilities.Tools, []string{"Bash", "Read"}) {
		t.Errorf("tools = %v, want Bash/Read", s.Capabilities.Tools)
	}
	if !equalStrings(s.Capabilities.PendingMCPServers, []string{"github"}) {
		t.Errorf("pending MCP = %v, want github", s.Capabilities.PendingMCPServers)
	}
	if !equalStrings(s.Capabilities.Agents, []string{"general-purpose"}) {
		t.Errorf("agents = %v, want general-purpose", s.Capabilities.Agents)
	}
	if !equalStrings(s.Capabilities.Skills, []string{"gavel-runner", "iconography"}) {
		t.Errorf("skills = %v, want extracted skill names", s.Capabilities.Skills)
	}
	if s.Budget == nil || s.Budget.Used != 1.25 || s.Budget.Total != 5.0 || s.Budget.Remaining != 3.75 {
		t.Fatalf("budget = %+v, want transcript budget", s.Budget)
	}
	if s.Context == nil || s.Context.UsedTokens != 1500 || s.Context.WindowTokens != claudeContextWindow {
		t.Fatalf("context = %+v, want input+cache occupancy", s.Context)
	}
	if len(s.Events) != 4 {
		t.Fatalf("session events = %d, want 4", len(s.Events))
	}
	if len(s.Turns) != 1 {
		t.Fatalf("turns = %d, want 1: %+v", len(s.Turns), s.Turns)
	}
	turn := s.Turns[0]
	if turn.Budget == nil || turn.Budget.Used != 1.25 {
		t.Errorf("turn budget = %+v, want budget_usd", turn.Budget)
	}
	if len(turn.Events) != 2 || turn.Events[0].Type != "queue-operation" || turn.Events[1].Type != "budget_usd" {
		t.Errorf("turn events = %+v, want queue-operation and budget_usd", turn.Events)
	}
	if turn.Model != "claude-opus-4" || turn.StopReason != string(claude.StopReasonEndTurn) {
		t.Errorf("turn model/stop = %q/%q", turn.Model, turn.StopReason)
	}
	if !equalStrings(turn.MessageIDs, []string{"user-1", "assistant-1"}) {
		t.Errorf("turn messages = %v, want user and assistant ids", turn.MessageIDs)
	}
	for _, msg := range s.Messages {
		if msg.ID == "user-1" || msg.ID == "assistant-1" {
			if msg.TurnID != turn.ID {
				t.Errorf("message %s turnId = %q, want %q", msg.ID, msg.TurnID, turn.ID)
			}
		}
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
