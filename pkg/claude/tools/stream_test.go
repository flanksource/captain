package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSystemInitTool_NameCategoryPretty(t *testing.T) {
	tool := &SystemInitTool{BaseTool: BaseTool{
		RawTool: "SessionInit",
		Input: map[string]any{
			"cwd":     "/tmp/project",
			"model":   "claude-sonnet-4",
			"tools":   []any{"Bash", "Read", "Edit"},
			"plugins": []any{"a", "b"},
		},
	}}
	assert.Equal(t, "SessionInit", tool.Name())
	assert.Equal(t, "system", tool.Category())
	pretty := tool.Pretty().String()
	assert.Contains(t, pretty, "init")
	assert.Contains(t, pretty, "claude-sonnet-4")
	assert.Contains(t, pretty, "/tmp/project")
}

func TestHookStartedTool_NameCategoryPretty(t *testing.T) {
	tool := &HookStartedTool{BaseTool: BaseTool{
		RawTool: "HookStart",
		Input: map[string]any{
			"hook_name":  "SessionStart:startup",
			"hook_event": "SessionStart",
		},
	}}
	assert.Equal(t, "HookStart", tool.Name())
	assert.Equal(t, "hook", tool.Category())
	pretty := tool.Pretty().String()
	assert.Contains(t, pretty, "SessionStart:startup")
}

func TestHookResponseTool_NameCategoryPretty(t *testing.T) {
	tool := &HookResponseTool{BaseTool: BaseTool{
		RawTool: "HookResponse",
		Input: map[string]any{
			"hook_name": "SessionStart:startup",
			"outcome":   "success",
			"exit_code": float64(0),
			"stdout":    "OK\n",
			"stderr":    "",
		},
	}}
	assert.Equal(t, "HookResponse", tool.Name())
	assert.Equal(t, "hook", tool.Category())
	pretty := tool.Pretty().String()
	assert.Contains(t, pretty, "SessionStart:startup")
	assert.Contains(t, pretty, "success")
}

func TestResultSummaryTool_NameCategoryPretty(t *testing.T) {
	tool := &ResultSummaryTool{BaseTool: BaseTool{
		RawTool: "Result",
		Input: map[string]any{
			"num_turns":      float64(3),
			"total_cost_usd": 0.0123,
			"duration_ms":    float64(4500),
			"is_error":       false,
			"result":         "Done.",
		},
	}}
	assert.Equal(t, "Result", tool.Name())
	assert.Equal(t, "result", tool.Category())
	pretty := tool.Pretty().String()
	assert.Contains(t, pretty, "result")
	assert.Contains(t, pretty, "3")     // num_turns
	assert.Contains(t, pretty, "0.012") // cost
}

func TestNewTool_DispatchSyntheticTypes(t *testing.T) {
	for _, name := range []string{
		"SessionInit", "HookStart", "HookResponse", "Result",
		"TokenCount", "TaskStarted", "TaskComplete", "TurnAborted",
		"ContextCompacted", "ThreadRolledBack", "ItemCompleted",
		"CodexExecCommand", "UserShellCommand", "CodexPatchApply", "MCPToolCall",
		"WebSearchEvent", "ViewImage", "GuardianAssessment", "ReviewMode",
		"CollabAgentSpawn", "CollabAgentInteraction", "CollabWaiting",
		"CollabClose", "QueueOperation", "DeferredToolsDelta",
		"AgentListingDelta", "SkillListing", "Budget", "PrLink",
		"CompactBoundary", "LocalCommand", "ScheduledTaskFire",
		"Informational", "WorktreeState", "Relocated", "Started",
		"ClaudeCommand", "GoalStatus",
	} {
		got := NewTool(BaseTool{RawTool: name})
		assert.Equal(t, name, got.Name(), "expected NewTool to return the right concrete type for %q", name)
	}
}

// TestSkillListingTool_PrettyEmitsSingleCount guards against the row rendering
// "count=N count=N" when a listing carries both the names array and the
// redundant skillCount scalar, as Claude Code transcripts do.
func TestSkillListingTool_PrettyEmitsSingleCount(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input map[string]any
		want  string
	}{
		{
			name:  "names and skillCount both present",
			input: map[string]any{"names": []any{"a", "b"}, "skillCount": float64(29)},
			want:  " count=2",
		},
		{
			name:  "only skillCount present",
			input: map[string]any{"skillCount": float64(29)},
			want:  " count=29",
		},
		{
			name:  "only names present",
			input: map[string]any{"names": []any{"a", "b", "c"}},
			want:  " count=3",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pretty := NewTool(BaseTool{RawTool: "SkillListing", Input: tc.input}).Pretty().String()
			assert.Equal(t, 1, strings.Count(pretty, "count="),
				"expected exactly one count= field in %q", pretty)
			assert.Contains(t, pretty, tc.want)
		})
	}
}

func TestUserShellCommandTool_PrettyAndDetail(t *testing.T) {
	tool := NewTool(BaseTool{
		RawTool: "UserShellCommand",
		Input: map[string]any{
			"command":     "gavel proc restart",
			"exit_code":   1,
			"duration_ms": 2990.9,
			"stdout":      "Kill sent but port 8088 is still bound",
		},
	})
	if _, ok := tool.(*UserShellCommandTool); !ok {
		t.Fatalf("NewTool returned %T, want *UserShellCommandTool", tool)
	}
	pretty := tool.Pretty().String()
	assert.Contains(t, pretty, "local command")
	assert.Contains(t, pretty, "gavel proc restart")
	assert.Contains(t, pretty, "exit=1")
	assert.Contains(t, pretty, "3.0s")
	detail := tool.Detail()
	if assert.NotNil(t, detail) {
		assert.Contains(t, detail.String(), "Kill sent but port 8088 is still bound")
	}
}
