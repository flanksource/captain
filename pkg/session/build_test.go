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

func TestBuildSessionIdentityPrefersLatestRootTitle(t *testing.T) {
	root := claude.ParsedTranscript{
		Path: "/p/root-sess.jsonl",
		Entries: []claude.HistoryEntry{{
			SessionID: "root-sess",
			Slug:      "fallback-title-slug",
			Message: claude.Message{
				Role:    claude.MessageRoleUser,
				Content: []claude.ContentBlock{{Type: claude.ContentTypeText, Text: "Inspect the session viewer"}},
			},
		}},
		ToolUses: []claude.ToolUse{
			{Tool: "SessionTitle", Input: map[string]any{"aiTitle": "Initial generated title"}},
			{Tool: "SessionTitle", Input: map[string]any{"aiTitle": "Updated generated title"}},
		},
	}

	s := buildSession(claude.ParsedSession{SessionID: "root-sess", Transcripts: []claude.ParsedTranscript{root}})
	if s.Title != "Updated generated title" {
		t.Fatalf("title = %q, want latest root title", s.Title)
	}
	if s.InitialPrompt != "Inspect the session viewer" {
		t.Fatalf("initial prompt = %q", s.InitialPrompt)
	}
}

func TestApprovalStats(t *testing.T) {
	uses := []claude.ToolUse{
		{Tool: "Write", ToolUseID: "1"},
		{Tool: "Bash", ToolUseID: "2", Denied: true, DeniedReason: "no rm -rf"},
		{Tool: "ExitPlanMode", ToolUseID: "3"},
		{Tool: "Plan", ToolUseID: "4"},
		{Tool: "MemoryCitation", ToolUseID: "5"},
	}
	got := approvalStats(uses)
	// A transcript has no approval signal, only a denial one, so an approval
	// count derived from it is fabricated. The database projection supplies the
	// real figure from captain_turn_requests.
	if got.Approved != 0 {
		t.Errorf("approved = %d, want 0 (a transcript cannot know)", got.Approved)
	}
	if got.Denied != 1 || len(got.Denials) != 1 || got.Denials[0].Reason != "no rm -rf" {
		t.Errorf("denials = %+v, want one with reason 'no rm -rf'", got.Denials)
	}
}

func TestCostFromUsage_OpusPricing(t *testing.T) {
	c := CostFromUsage(&claude.Usage{InputTokens: 1000, OutputTokens: 500}, "claude-opus-4-5")
	// opus 4.5: input 5/Mtok, output 25/Mtok → 0.005 + 0.0125
	if want := 0.0175; math.Abs(c.Total()-want) > 1e-9 {
		t.Errorf("total = %v, want %v", c.Total(), want)
	}
	if c.InputTokens != 1000 || c.OutputTokens != 500 {
		t.Errorf("tokens = %d/%d, want 1000/500", c.InputTokens, c.OutputTokens)
	}
}

// TestCostFromUsage_UnpricedModel pins that a model the catalog does not price
// keeps its token counts and reports no cost, rather than being billed at some
// other model's rate.
func TestCostFromUsage_UnpricedModel(t *testing.T) {
	c := CostFromUsage(&claude.Usage{InputTokens: 1000, OutputTokens: 500}, "some-retired-model")
	if c.Total() != 0 {
		t.Errorf("total = %v, want 0 for an unpriced model", c.Total())
	}
	if c.InputTokens != 1000 || c.OutputTokens != 500 {
		t.Errorf("tokens = %d/%d, want 1000/500 preserved", c.InputTokens, c.OutputTokens)
	}
}

func TestBuildSession_CostFilesPlanApprovals(t *testing.T) {
	writeBlock := toolUseBlock("w1", "Write", rawInput(t, map[string]any{"file_path": "/repo/x.go", "content": "package x"}))
	planBlock := toolUseBlock("p1", "ExitPlanMode", rawInput(t, map[string]any{"planFilePath": "/home/u/.claude/plans/foo.md", "plan": "do X"}))
	e := assistantEntry("a1", "", "claude-opus-4-5",
		&claude.Usage{InputTokens: 1000, OutputTokens: 500},
		claude.ContentBlock{Type: claude.ContentTypeText, Text: "hello"},
		claude.ContentBlock{Type: claude.ContentTypeText, Text: "<proposed_plan>tagged fallback</proposed_plan>"},
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
	if want := 0.0175; math.Abs(s.Cost.Total()-want) > 1e-9 {
		t.Errorf("session cost = %v, want %v", s.Cost.Total(), want)
	}
	if s.Usage.InputTokens != 1000 {
		t.Errorf("usage input = %d, want 1000", s.Usage.InputTokens)
	}
	if want := []string{"x.go"}; !equalStrings(s.Files.Written, want) {
		t.Errorf("written = %v, want %v", s.Files.Written, want)
	}
	if s.Plan == nil || !s.Plan.Explicit || s.Plan.Path != "/home/u/.claude/plans/foo.md" || s.Plan.Content != "do X" {
		t.Errorf("plan = %+v, want explicit foo.md", s.Plan)
	}
	if len(s.Messages) == 0 || s.Messages[0].Role != "assistant" {
		t.Errorf("messages = %+v, want a leading assistant message", s.Messages)
	}
	// Neither tool was denied, and a transcript records nothing else about
	// approvals, so the session claims none rather than inventing them.
	if s.Approvals.Approved != 0 || s.Approvals.Denied != 0 {
		t.Errorf("approvals = %+v, want none from a transcript with no denials", s.Approvals)
	}
}

func TestBuildSession_TaggedPlanFallbackAndMemoryCitation(t *testing.T) {
	entry := assistantEntry("tagged-1", "", "claude-opus-4-5", nil,
		claude.ContentBlock{Type: claude.ContentTypeText, Text: `<proposed_plan>
# Shared fallback
</proposed_plan>

After the plan.

<oai-mem-citation>
<citation_entries>
MEMORY.md:10-12|note=[shared parser]
</citation_entries>
<rollout_ids>
019f3754-ecfa-7323-a76b-a0205ea30bbe
</rollout_ids>
</oai-mem-citation>`},
	)
	uses := claude.ExtractToolUses([]claude.HistoryEntry{entry})
	s := buildSession(claude.ParsedSession{
		SessionID: "root-sess",
		Transcripts: []claude.ParsedTranscript{{
			Path:     "/p/root-sess.jsonl",
			Entries:  []claude.HistoryEntry{entry},
			ToolUses: uses,
		}},
	})

	if s.Plan == nil || s.Plan.Content != "# Shared fallback" || !s.Plan.Explicit {
		t.Fatalf("plan = %+v", s.Plan)
	}
	if len(s.Events) != 1 || s.Events[0].Type != "memory_citation" || s.Events[0].TurnID == "" {
		t.Fatalf("events = %+v", s.Events)
	}
	if len(s.Messages) != 1 || len(s.Messages[0].Parts) != 2 {
		t.Fatalf("messages = %+v", s.Messages)
	}
	if s.Messages[0].Parts[0].ToolName != "Plan" || s.Messages[0].Parts[1].Text != "After the plan." {
		t.Fatalf("parts = %+v", s.Messages[0].Parts)
	}
}

func TestBuildSession_EnvelopeRendersSummary(t *testing.T) {
	entry := assistantEntry("env-1", "", "claude-opus-4-5", nil,
		claude.ContentBlock{Type: claude.ContentTypeText, Text: `{"endStatus":"completed","plan":{"content":"","path":"/Users/moshe/.codex/plans/x.md","status":"new"},"questions":[],"summary":"Authored the review-banner plan."}`},
	)
	uses := claude.ExtractToolUses([]claude.HistoryEntry{entry})
	s := buildSession(claude.ParsedSession{
		SessionID: "root-sess",
		Transcripts: []claude.ParsedTranscript{{
			Path:     "/p/root-sess.jsonl",
			Entries:  []claude.HistoryEntry{entry},
			ToolUses: uses,
		}},
	})

	if len(s.Messages) != 1 || len(s.Messages[0].Parts) != 1 {
		t.Fatalf("messages = %+v", s.Messages)
	}
	part := s.Messages[0].Parts[0]
	if part.Type != PartText || part.Text != "Authored the review-banner plan." {
		t.Fatalf("part = %+v, want plain summary text", part)
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
				Model:      "claude-opus-4-5",
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
	if turn.Model != "claude-opus-4-5" || turn.StopReason != string(claude.StopReasonEndTurn) {
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

func TestBuildSessionKeepsConcurrentAgentTurnsSeparate(t *testing.T) {
	rootEntries := []claude.HistoryEntry{
		claudeTurnEntry("root-user", "2026-07-14T12:00:00Z", claude.MessageRoleUser, ""),
		claudeTurnEntry("root-assistant", "2026-07-14T12:00:05Z", claude.MessageRoleAssistant, claude.StopReasonEndTurn),
	}
	agentEntries := []claude.HistoryEntry{
		claudeTurnEntry("agent-user", "2026-07-14T12:00:01Z", claude.MessageRoleUser, ""),
		claudeTurnEntry("agent-assistant", "2026-07-14T12:00:04Z", claude.MessageRoleAssistant, claude.StopReasonEndTurn),
	}
	s := buildSession(claude.ParsedSession{
		SessionID: "root-sess",
		Transcripts: []claude.ParsedTranscript{
			{Path: "/p/root-sess.jsonl", Entries: rootEntries},
			{Path: "/p/root-sess/subagents/agent-child.jsonl", IsAgent: true, AgentID: "child", Entries: agentEntries},
		},
	})

	if len(s.Turns) != 2 {
		t.Fatalf("turns = %+v, want independent root and child turns", s.Turns)
	}
	if s.Turns[0].AgentID != "root-sess" || s.Turns[0].ID != "root-sess/turn-1" {
		t.Fatalf("first turn = %+v, want namespaced root turn", s.Turns[0])
	}
	if s.Turns[1].AgentID != "child" || s.Turns[1].ID != "child/turn-1" {
		t.Fatalf("second turn = %+v, want namespaced child turn", s.Turns[1])
	}
	turnByMessage := map[string]string{}
	for _, message := range s.Messages {
		turnByMessage[message.ID] = message.TurnID
	}
	if turnByMessage["root-assistant"] != "root-sess/turn-1" || turnByMessage["agent-assistant"] != "child/turn-1" {
		t.Fatalf("message turns = %+v", turnByMessage)
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
