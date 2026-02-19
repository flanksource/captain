package history

import (
	"fmt"
	"strings"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
)

type CodexCommand struct {
	Command string `json:"command"`
	Output  string `json:"output,omitempty"`
}

func (c CodexCommand) ToolName() string { return "CodexCommand" }

func (c CodexCommand) Pretty() api.Text {
	text := clicky.Text("").
		Add(icons.Icon{Unicode: "💻", Iconify: "codicon:terminal", Style: "muted"}).
		Append(" Bash", "text-green-600 font-medium")
	if c.Command != "" {
		text = text.NewLine().Add(clicky.CodeBlock(c.Command, "bash"))
	}
	if c.Output != "" {
		preview := c.Output
		lines := strings.Split(preview, "\n")
		if len(lines) > 20 {
			preview = strings.Join(lines[:20], "\n") + fmt.Sprintf("\n... (%d more lines)", len(lines)-20)
		}
		text = text.NewLine().Add(clicky.CodeBlock(preview, ""))
	}
	return text
}

type CodexReasoning struct {
	Text string `json:"text"`
}

func (r CodexReasoning) ToolName() string { return "CodexReasoning" }

func (r CodexReasoning) Pretty() api.Text {
	return clicky.Text("").
		Add(icons.Icon{Unicode: "💭", Iconify: "mdi:thought-bubble", Style: "muted"}).
		Append(" ", "").
		Append(r.Text, "text-gray-500 italic")
}

type CodexMessage struct {
	Text string `json:"text"`
}

func (m CodexMessage) ToolName() string { return "CodexMessage" }

func (m CodexMessage) Pretty() api.Text {
	text := clicky.Text("").
		Add(icons.Icon{Unicode: "🤖", Iconify: "mdi:robot", Style: "muted"}).
		Append(" Assistant", "text-blue-600 font-medium")
	if m.Text != "" {
		text = text.NewLine().Append(m.Text, "text-gray-700")
	}
	return text
}

type CodexTokenSummary struct {
	InputTokens       int `json:"input_tokens"`
	CachedInputTokens int `json:"cached_input_tokens"`
	OutputTokens      int `json:"output_tokens"`
	TotalTokens       int `json:"total_tokens"`
}

func (t CodexTokenSummary) ToolName() string { return "CodexTokenSummary" }

func (t CodexTokenSummary) Pretty() api.Text {
	return clicky.Text("").
		Add(icons.Icon{Unicode: "📊", Iconify: "mdi:chart-bar", Style: "muted"}).
		Append(fmt.Sprintf(" Tokens: %d in (%d cached) / %d out / %d total",
			t.InputTokens, t.CachedInputTokens, t.OutputTokens, t.TotalTokens), "text-gray-500")
}

var CodexTools = []Tool{
	CodexCommand{},
	CodexReasoning{},
	CodexMessage{},
	CodexTokenSummary{},
}
