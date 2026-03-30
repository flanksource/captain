package tools

import (
	"fmt"

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

func (t *TaskCreateTool) Name() string        { return "Task" }
func (t *TaskCreateTool) FilePath() string    { return "" }
func (t *TaskCreateTool) ExtractPath() string { return "" }
func (t *TaskCreateTool) Category() string    { return "" }
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
func (t *TodoWriteTool) Detail() api.Textable { return nil }

func (t *TodoWriteTool) Pretty() api.Text {
	text := t.header(icons.Package, "task", "text-indigo-400 font-medium")
	count := 0
	if todos, ok := t.Input["todos"].([]any); ok {
		count = len(todos)
	}
	return text.Append(fmt.Sprintf(" (%d items)", count), "text-gray-500")
}

type SkillTool struct{ BaseTool }

func (t *SkillTool) Name() string        { return "Skill" }
func (t *SkillTool) FilePath() string    { return "" }
func (t *SkillTool) ExtractPath() string { return "" }
func (t *SkillTool) Category() string    { return "" }
func (t *SkillTool) Detail() api.Textable { return nil }

func (t *SkillTool) Pretty() api.Text {
	text := t.header(icons.Info, "skill", "text-teal-400 font-medium")
	return text.Append(" "+t.Str("skill"), "max-w-[tw-20ch]")
}
