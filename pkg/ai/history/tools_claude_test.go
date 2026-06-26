package history

import (
	"strings"
	"testing"
)

// render is a tiny helper that renders a ToolUse to its plain (style-stripped)
// string for substring assertions.
func render(tool string, input map[string]any) string {
	return ToolUse{Tool: tool, Input: input}.Pretty().String()
}

func TestPrettyCoversClaudeCodeTools(t *testing.T) {
	cases := []struct {
		name  string
		tool  string
		input map[string]any
		want  []string // substrings that must all appear
	}{
		{
			name:  "Agent shows description and subagent type",
			tool:  "Agent",
			input: map[string]any{"subagent_type": "Explore", "description": "Explore todos", "prompt": "long prompt body"},
			want:  []string{"agent", "Explore todos", "Explore"},
		},
		{
			name:  "TaskCreate shows subject",
			tool:  "TaskCreate",
			input: map[string]any{"subject": "alias theme", "description": "phase 1", "activeForm": "Aliasing"},
			want:  []string{"task-create", "alias theme"},
		},
		{
			name:  "TaskUpdate shows id and status",
			tool:  "TaskUpdate",
			input: map[string]any{"taskId": "1", "status": "in_progress"},
			want:  []string{"task-update", "1", "in_progress"},
		},
		{
			name:  "TaskStop shows task id",
			tool:  "TaskStop",
			input: map[string]any{"task_id": "bctda0orx"},
			want:  []string{"task-stop", "bctda0orx"},
		},
		{
			name:  "TaskGet shows id",
			tool:  "TaskGet",
			input: map[string]any{"id": "2"},
			want:  []string{"task-get", "2"},
		},
		{
			name:  "ToolSearch shows query",
			tool:  "ToolSearch",
			input: map[string]any{"query": "select:ExitPlanMode", "max_results": float64(1)},
			want:  []string{"tool-search", "select:ExitPlanMode"},
		},
		{
			name:  "AskUserQuestion shows count and header",
			tool:  "AskUserQuestion",
			input: map[string]any{"questions": []any{map[string]any{"question": "How?", "header": "Launch mechanism"}}},
			want:  []string{"ask-user-question", "1 questions", "Launch mechanism"},
		},
		{
			name:  "Monitor shows description and command",
			tool:  "Monitor",
			input: map[string]any{"description": "PR 57 checks", "command": "gh pr checks 57", "timeout_ms": float64(1800000)},
			want:  []string{"monitor", "PR 57 checks", "gh pr checks 57"},
		},
		{
			name:  "ScheduleWakeup shows delay and reason",
			tool:  "ScheduleWakeup",
			input: map[string]any{"delaySeconds": float64(900), "reason": "Fallback heartbeat"},
			want:  []string{"schedule-wakeup", "900s", "Fallback heartbeat"},
		},
		{
			name:  "ExitPlanMode shows plan file path",
			tool:  "ExitPlanMode",
			input: map[string]any{"plan": "# big plan", "planFilePath": "/tmp/plans/clean.md"},
			want:  []string{"exit-plan-mode", "clean.md"},
		},
		{
			name:  "DesignSync shows method",
			tool:  "DesignSync",
			input: map[string]any{"method": "list_projects"},
			want:  []string{"design-sync", "list_projects"},
		},
		{
			name:  "PushNotification shows message",
			tool:  "PushNotification",
			input: map[string]any{"message": "PR #52 passes CI", "status": "proactive"},
			want:  []string{"push-notification", "PR #52 passes CI"},
		},
		{
			name:  "Workflow shows meta name",
			tool:  "Workflow",
			input: map[string]any{"script": "export const meta = {\n  name: 'review-cel',\n  description: 'x',\n}"},
			want:  []string{"workflow", "review-cel"},
		},
		{
			name:  "TaskList renders without args",
			tool:  "TaskList",
			input: map[string]any{},
			want:  []string{"task-list"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := render(tc.tool, tc.input)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("render(%s) = %q, missing %q", tc.tool, got, w)
				}
			}
		})
	}
}

func TestPrettyRendersDescriptionOnce(t *testing.T) {
	// Regression: a generic pre-render + per-case render used to emit the
	// description twice for tools that carry a `description` field.
	for _, tool := range []string{"Task", "Agent", "Monitor"} {
		input := map[string]any{"description": "unique-marker-xyz", "command": "ls", "subagent_type": "Explore"}
		got := render(tool, input)
		if n := strings.Count(got, "unique-marker-xyz"); n != 1 {
			t.Errorf("render(%s) rendered description %d times, want 1: %q", tool, n, got)
		}
	}
}

func TestPrettyCoversMcpTools(t *testing.T) {
	cases := []struct {
		tool  string
		input map[string]any
		want  []string
	}{
		{
			tool:  "mcp__playwright__browser_navigate",
			input: map[string]any{"url": "https://example.com"},
			want:  []string{"playwright", "browser_navigate", "https://example.com"},
		},
		{
			tool:  "mcp__postgres__execute_sql",
			input: map[string]any{"sql": "select 1"},
			want:  []string{"postgres", "execute_sql", "select 1"},
		},
		{
			tool:  "mcp__iconify__search_icons",
			input: map[string]any{"query": "home"},
			want:  []string{"iconify", "search_icons", "home"},
		},
	}

	for _, tc := range cases {
		got := render(tc.tool, tc.input)
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("render(%s) = %q, missing %q", tc.tool, got, w)
			}
		}
	}
}

func TestToolLabelHumanizesAndSplitsMcp(t *testing.T) {
	cases := map[string]string{
		"Bash":                              "bash",
		"WebFetch":                          "web-fetch",
		"TaskUpdate":                        "task-update",
		"AskUserQuestion":                   "ask-user-question",
		"mcp__playwright__browser_navigate": "playwright browser_navigate",
	}
	for tool, want := range cases {
		if got := toolLabel(tool); got != want {
			t.Errorf("toolLabel(%q) = %q, want %q", tool, got, want)
		}
	}
}

func TestFormatToolUseSummaryCoversNewTools(t *testing.T) {
	cases := []struct {
		tool  string
		input map[string]any
		want  string
	}{
		{"Agent", map[string]any{"description": "Explore todos"}, "Explore todos"},
		{"TaskUpdate", map[string]any{"taskId": "1", "status": "in_progress"}, "in_progress"},
		{"ToolSearch", map[string]any{"query": "select:Read"}, "select:Read"},
		{"mcp__postgres__execute_sql", map[string]any{"sql": "select 1"}, "select 1"},
	}
	for _, tc := range cases {
		got := FormatToolUseSummary(tc.tool, tc.input)
		if !strings.Contains(got, tc.want) {
			t.Errorf("FormatToolUseSummary(%s) = %q, missing %q", tc.tool, got, tc.want)
		}
	}
}
