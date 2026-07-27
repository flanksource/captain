package session

import (
	"testing"

	"github.com/flanksource/captain/pkg/claude/tools"
)

type todoCall struct {
	tool  string
	input map[string]any
}

func callAccessor(c todoCall) (string, map[string]any) { return c.tool, c.input }

func claudeTodos(items ...map[string]any) map[string]any {
	raw := make([]any, 0, len(items))
	for _, item := range items {
		raw = append(raw, item)
	}
	return map[string]any{"todos": raw}
}

func TestLatestTodosNormalizesProviderShapes(t *testing.T) {
	tests := []struct {
		name  string
		calls []todoCall
		want  []tools.TodoItem
	}{
		{
			name: "claude content and activeForm",
			calls: []todoCall{{tool: "TodoWrite", input: claudeTodos(
				map[string]any{"content": "Wire the ingest", "status": "in_progress"},
				map[string]any{"activeForm": "Backfilling rows", "status": "pending"},
			)}},
			want: []tools.TodoItem{
				{Text: "Wire the ingest", Status: "in_progress"},
				{Text: "Backfilling rows", Status: "pending"},
			},
		},
		{
			// The shape Claude Code actually emits: content and activeForm on the
			// same item. content is the imperative form and wins.
			name: "content wins over activeForm when both are present",
			calls: []todoCall{{tool: "TodoWrite", input: claudeTodos(
				map[string]any{
					"content":    "Add Commits and Files tabs to PR detail view",
					"status":     "pending",
					"activeForm": "Adding Commits and Files tabs to PR detail view",
				},
			)}},
			want: []tools.TodoItem{{Text: "Add Commits and Files tabs to PR detail view", Status: "pending"}},
		},
		{
			name: "codex update_plan with step under plan key",
			calls: []todoCall{{tool: "update_plan", input: map[string]any{
				"plan": []any{map[string]any{"step": "Classify mutations", "status": "completed"}},
			}}},
			want: []tools.TodoItem{{Text: "Classify mutations", Status: "completed"}},
		},
		{
			name: "later call supersedes earlier",
			calls: []todoCall{
				{tool: "TodoWrite", input: claudeTodos(map[string]any{"content": "First", "status": "pending"})},
				{tool: "Bash", input: map[string]any{"command": "go test ./..."}},
				{tool: "TodoWrite", input: claudeTodos(map[string]any{"content": "Second", "status": "completed"})},
			},
			want: []tools.TodoItem{{Text: "Second", Status: "completed"}},
		},
		{
			name: "empty payload falls back to the previous non-empty call",
			calls: []todoCall{
				{tool: "TodoWrite", input: claudeTodos(map[string]any{"content": "Kept", "status": "pending"})},
				{tool: "TodoWrite", input: map[string]any{"todos": []any{}}},
			},
			want: []tools.TodoItem{{Text: "Kept", Status: "pending"}},
		},
		{
			name:  "no task-list calls yields nothing",
			calls: []todoCall{{tool: "Read", input: map[string]any{"file_path": "main.go"}}},
			want:  nil,
		},
		{
			name: "entries without usable text are skipped",
			calls: []todoCall{{tool: "TodoWrite", input: claudeTodos(
				map[string]any{"status": "pending"},
				map[string]any{"content": "Real item"},
			)}},
			want: []tools.TodoItem{{Text: "Real item"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := latestTodos(tt.calls, callAccessor)
			if len(got) != len(tt.want) {
				t.Fatalf("latestTodos() returned %d items, want %d: %+v", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("item %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}
