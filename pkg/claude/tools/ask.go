package tools

import (
	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
)

var askIcon = icons.Icon{Unicode: "❓", Iconify: "mdi:help-circle", Style: "muted"}

type AskTool struct{ BaseTool }

func (t *AskTool) Name() string        { return "Ask" }
func (t *AskTool) Category() string    { return "" }
func (t *AskTool) FilePath() string    { return "" }
func (t *AskTool) ExtractPath() string { return "" }

func (t *AskTool) Pretty() api.Text {
	text := t.header(askIcon, "ask", "text-amber-400 font-medium")
	if q := t.firstQuestion(); q != "" {
		text = text.Append(" "+q, "max-w-[tw-20ch]")
	}
	return text
}

func (t *AskTool) Detail() api.Textable {
	q := t.firstQuestion()
	if q == "" && t.Response == "" {
		return nil
	}
	text := clicky.Text("").Append("Q: ", "font-bold").Append(q, "")
	if t.Response != "" {
		text = text.NewLine().Append("A: ", "font-bold text-green-500").Append(t.Response, "")
	}
	return &text
}

func (t *AskTool) firstQuestion() string {
	if q, ok := t.Input["question"].(string); ok && q != "" {
		return q
	}
	questions, ok := t.Input["questions"].([]any)
	if !ok || len(questions) == 0 {
		return ""
	}
	first := questions[0]
	if m, ok := first.(map[string]any); ok {
		if q, ok := m["question"].(string); ok {
			return q
		}
	}
	if s, ok := first.(string); ok {
		return s
	}
	return ""
}
