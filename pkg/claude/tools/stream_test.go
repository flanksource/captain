package tools

import (
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
	for _, name := range []string{"SessionInit", "HookStart", "HookResponse", "Result"} {
		got := NewTool(BaseTool{RawTool: name})
		assert.Equal(t, name, got.Name(), "expected NewTool to return the right concrete type for %q", name)
	}
}
