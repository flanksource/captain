package tools

import (
	"strings"
	"testing"
)

func TestTodoWriteTool_PrettyAndDetailIncludePlanItems(t *testing.T) {
	tool := &TodoWriteTool{BaseTool: BaseTool{
		RawTool: "TodoWrite",
		Input: map[string]any{
			"todos": []any{
				map[string]any{"step": "Inspect Arthas history", "status": "completed"},
				map[string]any{"content": "Run focused tests", "status": "in_progress"},
				map[string]any{"activeForm": "Rebuild captain binary", "status": "pending"},
			},
		},
	}}

	pretty := tool.Pretty().String()
	for _, want := range []string{
		"task",
		"3 items",
		"Inspect Arthas history",
		"Run focused tests",
		"+1 more",
	} {
		if !strings.Contains(pretty, want) {
			t.Fatalf("Pretty() missing %q: %s", want, pretty)
		}
	}

	detail := tool.Detail()
	if detail == nil {
		t.Fatal("Detail() returned nil")
	}
	rendered := detail.String()
	for _, want := range []string{
		"completed: Inspect Arthas history",
		"in_progress: Run focused tests",
		"pending: Rebuild captain binary",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Detail() missing %q: %s", want, rendered)
		}
	}
}
