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
		if item.status != "" {
			text = text.Append(item.status+": ", "text-gray-500")
		}
		text = text.Append(item.text, "")
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

type todoWriteItem struct {
	text   string
	status string
}

func (t *TodoWriteTool) todoItems() []todoWriteItem {
	raw := t.Input["todos"]
	if raw == nil {
		raw = t.Input["plan"]
	}
	return todoItems(raw)
}

func todoItems(raw any) []todoWriteItem {
	var out []todoWriteItem
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

func todoItem(raw any) (todoWriteItem, bool) {
	m, ok := raw.(map[string]any)
	if !ok {
		return todoWriteItem{}, false
	}

	text := firstString(m, "step", "content", "activeForm", "title", "description")
	text = compactText(text)
	if text == "" {
		return todoWriteItem{}, false
	}
	return todoWriteItem{
		text:   truncateText(text, 160),
		status: compactText(firstString(m, "status")),
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

func todoPreview(items []todoWriteItem, max int) string {
	if len(items) == 0 || max <= 0 {
		return ""
	}
	parts := make([]string, 0, max)
	for _, item := range items {
		if item.text == "" {
			continue
		}
		parts = append(parts, truncateText(item.text, 60))
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
