package tools

import (
	"fmt"
	"strings"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
)

var agentIcon = icons.Icon{Unicode: "🤖", Iconify: "mdi:robot", Style: "muted"}

type AgentTool struct{ BaseTool }

func (t *AgentTool) Name() string        { return "Agent" }
func (t *AgentTool) FilePath() string    { return "" }
func (t *AgentTool) ExtractPath() string { return "" }
func (t *AgentTool) Category() string    { return "" }

func (t *AgentTool) Pretty() api.Text {
	text := t.header(agentIcon, "agent", "text-indigo-400 font-medium")
	label := t.Str("description")
	if label == "" {
		prompt := t.Str("prompt")
		if len(prompt) > 80 {
			prompt = prompt[:80]
		}
		label = prompt
	}
	text = text.Append(": ", "text-gray-400").Append(label, "text-gray-400 max-w-[tw-20ch]")
	if subagent := t.Str("subagent_type"); subagent != "" {
		text = text.Append(fmt.Sprintf(" (%s)", subagent), "text-gray-500")
	}
	return text
}

func (t *AgentTool) Detail() api.Textable {
	prompt := t.Str("prompt")
	if len(prompt) == 0 {
		return nil
	}
	if len(prompt) > 500 {
		prompt = prompt[:500]
	}
	return api.NewCode(prompt, "")
}

type TaskCreateTool struct{ BaseTool }

func (t *TaskCreateTool) Name() string         { return "Task" }
func (t *TaskCreateTool) FilePath() string     { return "" }
func (t *TaskCreateTool) ExtractPath() string  { return "" }
func (t *TaskCreateTool) Category() string     { return "" }
func (t *TaskCreateTool) Detail() api.Textable { return nil }

func (t *TaskCreateTool) Pretty() api.Text {
	text := t.header(icons.Package, "task", "text-indigo-400 font-medium")
	return text.Append(": ", "text-gray-400").Append(t.Str("subject"), "text-gray-400")
}

type TodoWriteTool struct{ BaseTool }

func (t *TodoWriteTool) Name() string        { return "Task" }
func (t *TodoWriteTool) FilePath() string    { return "" }
func (t *TodoWriteTool) ExtractPath() string { return "" }
func (t *TodoWriteTool) Category() string    { return "" }

func (t *TodoWriteTool) Detail() api.Textable {
	items := t.todoItems()
	if len(items) == 0 {
		return nil
	}

	text := clicky.Text("").Append("Plan", "font-bold")
	for _, item := range items {
		text = text.NewLine().Append("- ", "text-gray-500")
		if item.Status != "" {
			text = text.Append(item.Status+": ", "text-gray-500")
		}
		text = text.Append(item.Text, "")
	}
	return &text
}

func (t *TodoWriteTool) Pretty() api.Text {
	text := t.header(icons.Package, "task", "text-indigo-400 font-medium")
	items := t.todoItems()
	text = text.Append(fmt.Sprintf(" (%d items)", len(items)), "text-gray-500")
	if preview := todoPreview(items, 2); preview != "" {
		text = text.Append(": ", "text-gray-400").Append(preview, "text-gray-400")
	}
	return text
}

// TodoItem is one entry of an agent task list, normalized across the shapes the
// providers emit: Claude's TodoWrite uses content/activeForm, Codex's
// update_plan uses step. Persisted into captain_sessions.metadata["todos"], so
// the JSON tags are a storage contract.
type TodoItem struct {
	Text   string `json:"text"`
	Status string `json:"status,omitempty"`
}

func (t *TodoWriteTool) todoItems() []TodoItem {
	raw := t.Input["todos"]
	if raw == nil {
		raw = t.Input["plan"]
	}
	return TodoItems(raw)
}

// TodoItems normalizes a raw TodoWrite/update_plan payload into task items. It
// tolerates both []any and []map[string]any, and both provider key vocabularies.
// Entries without usable text are skipped rather than yielding empty items.
func TodoItems(raw any) []TodoItem {
	var out []TodoItem
	switch todos := raw.(type) {
	case []any:
		for _, todo := range todos {
			if item, ok := todoItem(todo); ok {
				out = append(out, item)
			}
		}
	case []map[string]any:
		for _, todo := range todos {
			if item, ok := todoItem(todo); ok {
				out = append(out, item)
			}
		}
	}
	return out
}

func todoItem(raw any) (TodoItem, bool) {
	m, ok := raw.(map[string]any)
	if !ok {
		return TodoItem{}, false
	}

	text := firstString(m, "step", "content", "activeForm", "title", "description")
	text = compactText(text)
	if text == "" {
		return TodoItem{}, false
	}
	return TodoItem{
		Text:   truncateText(text, 160),
		Status: compactText(firstString(m, "status")),
	}, true
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if s, ok := m[key].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func todoPreview(items []TodoItem, max int) string {
	if len(items) == 0 || max <= 0 {
		return ""
	}
	parts := make([]string, 0, max)
	for _, item := range items {
		if item.Text == "" {
			continue
		}
		parts = append(parts, truncateText(item.Text, 60))
		if len(parts) == max {
			break
		}
	}
	preview := strings.Join(parts, "; ")
	if len(items) > len(parts) {
		preview += fmt.Sprintf("; +%d more", len(items)-len(parts))
	}
	return preview
}

func compactText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncateText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

type SkillTool struct{ BaseTool }

func (t *SkillTool) Name() string         { return "Skill" }
func (t *SkillTool) FilePath() string     { return "" }
func (t *SkillTool) ExtractPath() string  { return "" }
func (t *SkillTool) Category() string     { return "" }
func (t *SkillTool) Detail() api.Textable { return nil }

func (t *SkillTool) Pretty() api.Text {
	text := t.header(icons.Info, "skill", "text-teal-400 font-medium")
	return text.Append(" "+t.Str("skill"), "max-w-[tw-20ch]")
}
