package tools

import (
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
)

// AssistantTool surfaces an assistant text turn (codex agent_message,
// claude assistant content text) as a row in the history output.
type AssistantTool struct{ BaseTool }

func (t *AssistantTool) Name() string        { return "Assistant" }
func (t *AssistantTool) Category() string    { return "message" }
func (t *AssistantTool) FilePath() string    { return "" }
func (t *AssistantTool) ExtractPath() string { return "" }

func (t *AssistantTool) Pretty() api.Text {
	icon := icons.Icon{Unicode: "🤖", Iconify: "mdi:robot", Style: "muted"}
	text := t.header(icon, "assistant", "text-blue-500 font-medium")
	if body := t.Str("text"); body != "" {
		text = text.Append(" "+body, "text-gray-700 max-w-[tw-20ch]")
	}
	return text
}

func (t *AssistantTool) Detail() api.Textable { return t.BaseTool.Detail() }

// ReasoningTool surfaces a reasoning/thinking summary (codex reasoning event,
// claude thinking blocks) so it shows up alongside other history rows.
type ReasoningTool struct{ BaseTool }

func (t *ReasoningTool) Name() string        { return "Reasoning" }
func (t *ReasoningTool) Category() string    { return "message" }
func (t *ReasoningTool) FilePath() string    { return "" }
func (t *ReasoningTool) ExtractPath() string { return "" }

func (t *ReasoningTool) Pretty() api.Text {
	icon := icons.Icon{Unicode: "💭", Iconify: "mdi:thought-bubble", Style: "muted"}
	text := t.header(icon, "reasoning", "text-purple-500 font-medium")
	if body := t.Str("text"); body != "" {
		text = text.Append(" "+body, "text-gray-500 italic max-w-[tw-20ch]")
	}
	return text
}

func (t *ReasoningTool) Detail() api.Textable { return t.BaseTool.Detail() }
