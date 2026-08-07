package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestBuildCodexSession_TaggedPlanPrecedesTodosAndCitationIsMetadata(t *testing.T) {
	ts := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	uses := []history.ToolUse{
		{Tool: "TodoWrite", Input: map[string]any{"todos": []any{
			map[string]any{"step": "short checklist", "status": "in_progress"},
		}}, Timestamp: &ts, SessionID: "cx-tagged", TurnID: "turn-1", Source: "codex"},
		{Tool: "Plan", Input: map[string]any{"content": "# Detailed plan\n\nShip it", "tag": "proposed_plan"}, Timestamp: &ts, SessionID: "cx-tagged", TurnID: "turn-1", Source: "codex"},
		{Tool: "Assistant", Input: map[string]any{"text": "Source used: https://example.com"}, Timestamp: &ts, SessionID: "cx-tagged", TurnID: "turn-1", Source: "codex"},
		{Tool: "MemoryCitation", Input: map[string]any{
			"event":            "memory_citation",
			"source":           "codex",
			"citation_entries": []string{"MEMORY.md:10-12|note=[parser seam]"},
			"rollout_ids":      []string{"019f3754-ecfa-7323-a76b-a0205ea30bbe"},
		}, Timestamp: &ts, SessionID: "cx-tagged", TurnID: "turn-1", Source: "codex"},
	}

	s := buildCodexSession(uses, &history.CodexSessionInfo{ID: "cx-tagged", CWD: "/repo"})
	if s.Plan == nil || s.Plan.Content != "# Detailed plan\n\nShip it" || !s.Plan.Explicit {
		t.Fatalf("plan = %+v", s.Plan)
	}
	if len(s.Plan.Events) != 1 || s.Plan.Events[0].Kind != PlanWrite {
		t.Fatalf("plan events = %+v", s.Plan.Events)
	}
	if len(s.Events) != 1 || s.Events[0].Type != "memory_citation" || s.Events[0].Scope != "session" || s.Events[0].TurnID != "turn-1" {
		t.Fatalf("session events = %+v", s.Events)
	}
	if got := s.Events[0].Data["citation_entries"]; len(got.([]string)) != 1 {
		t.Fatalf("citation entries = %#v", got)
	}
	var foundPlan, foundAssistant bool
	for _, message := range s.Messages {
		for _, part := range message.Parts {
			foundPlan = foundPlan || part.ToolName == "Plan"
			foundAssistant = foundAssistant || part.Text == "Source used: https://example.com"
		}
	}
	if !foundPlan || !foundAssistant {
		t.Fatalf("messages = %+v", s.Messages)
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
	if len(s.Turns) != 1 {
		t.Fatalf("turns = %+v, want one turn", s.Turns)
	}
	if got := s.Turns[0].ID; got != "turn-1" {
		t.Fatalf("turn id = %q, want turn-1", got)
	}
	if len(s.Turns[0].Events) != 2 || s.Turns[0].Events[0].Type != "task_started" || s.Turns[0].Events[1].Type != "task_complete" {
		t.Fatalf("turn events = %+v", s.Turns[0].Events)
	}
}

func TestBuildCodexSession_DerivesIdentityAfterSystemInstructions(t *testing.T) {
	stream := strings.Join([]string{
		`{"timestamp":"2026-07-10T09:49:37.000Z","type":"session_meta","payload":{"id":"sess-identity","cwd":"/repo"}}`,
		`{"timestamp":"2026-07-10T09:49:37.100Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions for /repo\n\nAlways test."}]}}`,
		`{"timestamp":"2026-07-10T09:49:37.200Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Improve   the Codex session parser\nwith a useful title"}]}}`,
	}, "\n")
	uses, err := history.ExtractCodexToolUsesFromReader(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("ExtractCodexToolUsesFromReader: %v", err)
	}

	s := buildCodexSession(uses, &history.CodexSessionInfo{ID: "sess-identity", CWD: "/repo"})
	if len(s.Messages) != 2 || s.Messages[0].Role != "system" || s.Messages[1].Role != "user" {
		t.Fatalf("message roles = %+v, want system then user", s.Messages)
	}
	if got, want := s.InitialPrompt, "Improve   the Codex session parser\nwith a useful title"; got != want {
		t.Fatalf("initial prompt = %q, want %q", got, want)
	}
	if got, want := s.Title, "Improve the Codex session parser with a useful title"; got != want {
		t.Fatalf("title = %q, want %q", got, want)
	}
}

func TestBuildCodexSession_RichCodexMetadata(t *testing.T) {
	stream := strings.Join([]string{
		`{"timestamp":"2026-07-09T06:13:17.184Z","type":"session_meta","payload":{"id":"rich-codex","cwd":"/repo","cli_version":"0.143.0","model_provider":"openai"}}`,
		`{"timestamp":"2026-07-09T06:13:17.197Z","type":"world_state","payload":{"full":true,"state":{"skills":{"includeInstructions":true}}}}`,
		`{"timestamp":"2026-07-09T06:13:17.197Z","type":"turn_context","payload":{"turn_id":"turn-rich","model":"gpt-5.5","effort":"xhigh"}}`,
		`{"timestamp":"2026-07-09T06:13:17.198Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-rich"}}`,
		`{"timestamp":"2026-07-09T06:13:18.000Z","type":"response_item","payload":{"type":"tool_search_call","call_id":"search-1","arguments":{"query":"multi-agent","limit":8},"internal_chat_message_metadata_passthrough":{"turn_id":"turn-rich"}}}`,
		`{"timestamp":"2026-07-09T06:13:18.010Z","type":"response_item","payload":{"type":"tool_search_output","call_id":"search-1","tools":[{"type":"namespace","name":"multi_agent_v1","tools":[{"type":"function","name":"spawn_agent"},{"type":"function","name":"wait_agent"}]}],"internal_chat_message_metadata_passthrough":{"turn_id":"turn-rich"}}}`,
		`{"timestamp":"2026-07-09T06:13:19.000Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":1000,"cached_input_tokens":300,"output_tokens":50,"reasoning_output_tokens":10,"total_tokens":1050},"total_token_usage":{"input_tokens":1000,"cached_input_tokens":300,"output_tokens":50,"reasoning_output_tokens":10,"total_tokens":1050},"model_context_window":2000}}}`,
		`{"timestamp":"2026-07-09T06:13:20.000Z","type":"response_item","payload":{"type":"function_call","name":"spawn_agent","namespace":"multi_agent_v1","arguments":"{\"agent_type\":\"worker\",\"message\":\"Fix lint\"}","call_id":"spawn-1","internal_chat_message_metadata_passthrough":{"turn_id":"turn-rich"}}}`,
		`{"timestamp":"2026-07-09T06:13:20.500Z","type":"response_item","payload":{"type":"function_call_output","call_id":"spawn-1","output":"{\"agent_id\":\"agent-1\",\"nickname\":\"Ada\"}","internal_chat_message_metadata_passthrough":{"turn_id":"turn-rich"}}}`,
		`{"timestamp":"2026-07-09T06:13:21.000Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"sed -n '1p' /repo/c.go\"}","call_id":"exec-1","internal_chat_message_metadata_passthrough":{"turn_id":"turn-rich"}}}`,
		`{"timestamp":"2026-07-09T06:13:21.500Z","type":"response_item","payload":{"type":"function_call_output","call_id":"exec-1","output":"ok","internal_chat_message_metadata_passthrough":{"turn_id":"turn-rich"}}}`,
		`{"timestamp":"2026-07-09T06:13:22.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-rich","duration_ms":4800}}`,
	}, "\n")
	uses, err := history.ExtractCodexToolUsesFromReader(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("ExtractCodexToolUsesFromReader: %v", err)
	}

	s := buildCodexSession(uses, &history.CodexSessionInfo{ID: "rich-codex", CWD: "/repo", Model: "gpt-5.5"})

	if got, want := s.Usage.InputTokens, 700; got != want {
		t.Fatalf("input tokens = %d, want %d", got, want)
	}
	if got, want := s.Usage.CacheReadTokens, 300; got != want {
		t.Fatalf("cache read tokens = %d, want %d", got, want)
	}
	// The buckets are disjoint: OpenAI reports reasoning as a subset of output, so
	// output nets down to 50-10 and reasoning carries the 10. Leaving output at 50
	// would double-count it in TotalTokens.
	if got, want := s.Usage.OutputTokens, 40; got != want {
		t.Fatalf("output tokens = %d, want %d", got, want)
	}
	if got, want := s.Usage.ReasoningTokens, 10; got != want {
		t.Fatalf("reasoning tokens = %d, want %d", got, want)
	}
	if s.Cost.TotalTokens != 1050 {
		t.Fatalf("total tokens = %d, want 1050", s.Cost.TotalTokens)
	}
	if got, want := s.Usage.TotalTokens(), 1050; got != want {
		t.Fatalf("summed usage buckets = %d, want %d", got, want)
	}
	if s.Context == nil || s.Context.UsedTokens != 1000 || s.Context.WindowTokens != 2000 || s.Context.FreePercent != 50 {
		t.Fatalf("context = %+v, want 1000/2000/50", s.Context)
	}
	if len(s.Turns) != 1 || s.Turns[0].ID != "turn-rich" || s.Turns[0].Usage.TotalTokens() != 1050 {
		t.Fatalf("turns = %+v", s.Turns)
	}
	if s.Turns[0].ReasoningEffort != "xhigh" {
		t.Fatalf("turn effort = %q, want xhigh", s.Turns[0].ReasoningEffort)
	}
	if len(s.Agents) != 2 || s.Agents[1].ID != "agent-1" || s.Agents[1].Type != "worker" || s.Agents[1].Desc != "Fix lint" {
		t.Fatalf("agents = %+v", s.Agents)
	}
	if want := []string{"spawn_agent", "wait_agent"}; !equalStrings(s.Capabilities.Tools, want) {
		t.Fatalf("tools = %v, want %v", s.Capabilities.Tools, want)
	}
	if want := []string{"worker"}; !equalStrings(s.Capabilities.Agents, want) {
		t.Fatalf("capability agents = %v, want %v", s.Capabilities.Agents, want)
	}
	if want := []string{"c.go"}; !equalStrings(s.Files.Read, want) {
		t.Fatalf("read files = %v, want %v", s.Files.Read, want)
	}
	var foundAgentTool bool
	for _, msg := range s.Messages {
		for _, part := range msg.Parts {
			if part.ToolName == "Agent" {
				foundAgentTool = true
			}
		}
	}
	if !foundAgentTool {
		t.Fatalf("messages did not include Agent tool part: %+v", s.Messages)
	}
	if len(s.Events) != 1 || s.Events[0].Type != "world_state" {
		t.Fatalf("session events = %+v, want world_state", s.Events)
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

func TestBuildCodex_IgnoresAutoReviewSession(t *testing.T) {
	file := filepath.Join(t.TempDir(), "review.jsonl")
	stream := strings.Join([]string{
		`{"timestamp":"2026-07-13T09:00:00Z","type":"session_meta","payload":{"id":"review-1","cwd":"/repo"}}`,
		`{"timestamp":"2026-07-13T09:00:01Z","type":"turn_context","payload":{"turn_id":"turn-review","model":"codex-auto-review","effort":"low"}}`,
		`{"timestamp":"2026-07-13T09:00:02Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"approved"}]}}`,
	}, "\n")
	require.NoError(t, os.WriteFile(file, []byte(stream), 0o600))

	assert.Empty(t, BuildCodex([]string{file}))
}

func TestBuildCodex_RecoversSessionIDFromRolloutFilename(t *testing.T) {
	const sessionID = "019edeb3-449e-7af3-b300-7123f10944b2"
	file := filepath.Join(t.TempDir(), "rollout-2026-06-19T10-05-51-"+sessionID+".jsonl")
	stream := `{"timestamp":"2026-06-19T07:05:52.061Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"check the change"}]}}`
	require.NoError(t, os.WriteFile(file, []byte(stream), 0o600))

	sessions := BuildCodex([]string{file})
	require.Len(t, sessions, 1)
	assert.Equal(t, sessionID, sessions[0].ID)
	require.NotNil(t, sessions[0].Root)
	assert.Equal(t, sessionID, sessions[0].Root.ID)
	require.Len(t, sessions[0].Messages, 1)
	require.NotNil(t, sessions[0].Messages[0].Provenance)
	assert.Equal(t, sessionID, sessions[0].Messages[0].Provenance.SessionID)
}

func TestBuildCodexSession_UserShellCommandIsNonOperationalEvent(t *testing.T) {
	ts := time.Date(2026, 7, 13, 6, 14, 1, 0, time.UTC)
	input := map[string]any{
		"event":            "user_shell_command",
		"command":          "gavel proc restart",
		"exit_code":        1,
		"duration_seconds": 2.9909,
		"duration_ms":      2990.9,
		"output":           "Kill sent but port 8088 is still bound",
		"stdout":           "Kill sent but port 8088 is still bound",
		"status":           "failed",
	}
	uses := []history.ToolUse{{
		Tool: "UserShellCommand", Input: input, Timestamp: &ts,
		SessionID: "sess-shell", Source: "codex",
	}}

	s := buildCodexSession(uses, &history.CodexSessionInfo{ID: "sess-shell", CWD: "/repo"})
	if len(s.Events) != 1 || s.Events[0].Type != "user_shell_command" {
		t.Fatalf("events = %+v, want one user_shell_command", s.Events)
	}
	if len(s.Messages) != 0 {
		t.Fatalf("messages = %+v, user shell command must not be an assistant tool call", s.Messages)
	}
	if s.Events[0].Data["command"] != "gavel proc restart" || s.Events[0].Data["exit_code"] != 1 || s.Events[0].Data["duration_ms"] != 2990.9 || s.Events[0].Data["output"] != "Kill sent but port 8088 is still bound" {
		t.Fatalf("event data = %+v", s.Events[0].Data)
	}
}

func TestBuildCodexSession_WaitIsConversationalToolWithOutput(t *testing.T) {
	ts := time.Date(2026, 7, 13, 9, 41, 33, 611000000, time.UTC)
	uses := []history.ToolUse{{
		Tool: "Wait",
		Input: map[string]any{
			"cell_id":       "214",
			"yield_time_ms": float64(20000),
			"max_tokens":    float64(5000),
		},
		Response: "evaluation failed\nexit=1", Timestamp: &ts,
		ToolUseID: "call-wait", TurnID: "turn-wait", SessionID: "sess-wait",
		Source: "codex", Model: "gpt-5.6-sol", ReasoningEffort: "max",
	}}

	s := buildCodexSession(uses, &history.CodexSessionInfo{ID: "sess-wait", CWD: "/repo"})
	if len(s.Events) != 0 {
		t.Fatalf("events = %+v, Wait must remain transcript activity", s.Events)
	}
	if len(s.Messages) != 1 || !IsConversationalMessage(s.Messages[0]) {
		t.Fatalf("messages = %+v, want one conversational Wait", s.Messages)
	}
	message := s.Messages[0]
	if message.TurnID != "turn-wait" || message.Provenance == nil || message.Provenance.Model != "gpt-5.6-sol" || message.Provenance.ReasoningEffort != "max" {
		t.Fatalf("metadata = %+v", message)
	}
	part := message.Parts[0]
	if part.ToolName != "Wait" || part.ToolCallID != "call-wait" || part.State != ToolStateOutputAvailable {
		t.Fatalf("part = %+v", part)
	}
	if string(part.Input) != `{"cell_id":"214","max_tokens":5000,"yield_time_ms":20000}` {
		t.Fatalf("input = %s", part.Input)
	}
	if string(part.Output) != `"evaluation failed\nexit=1"` {
		t.Fatalf("output = %s", part.Output)
	}
}

// A collapsed reasoning span must extend the session's end time: contentless
// reasoning is the only evidence the session was alive during a thinking burst,
// and last_activity_at is derived from s.EndedAt.
func TestBuildCodexSession_ReasoningSpanExtendsEndedAtButNotStartedAt(t *testing.T) {
	metaStart := time.Date(2026, 7, 16, 11, 14, 45, 0, time.UTC)
	spanLast := time.Date(2026, 7, 16, 11, 32, 58, 744000000, time.UTC)
	uses := []history.ToolUse{
		{
			Tool: "Reasoning",
			Input: map[string]any{
				"text":     "81 encrypted reasoning records over 17m54s",
				"first_at": "2026-07-16T11:15:04.024Z",
				"last_at":  "2026-07-16T11:32:58.744Z",
				"count":    81,
			},
			Timestamp: &spanLast,
			SessionID: "cx-span",
			Source:    "codex",
		},
	}
	info := &history.CodexSessionInfo{ID: "cx-span", CWD: "/repo", StartedAt: &metaStart}

	s := buildCodexSession(uses, info)

	require.NotNil(t, s.EndedAt)
	assert.True(t, s.EndedAt.Equal(spanLast), "EndedAt = %v, want span last %v", s.EndedAt, spanLast)
	require.NotNil(t, s.StartedAt)
	assert.True(t, s.StartedAt.Equal(metaStart), "StartedAt = %v, want session_meta %v", s.StartedAt, metaStart)

	require.Len(t, s.Messages, 1)
	require.Len(t, s.Messages[0].Parts, 1)
	part := s.Messages[0].Parts[0]
	assert.Equal(t, PartReasoning, part.Type)
	assert.Contains(t, part.Text, "81 encrypted reasoning records")
}
