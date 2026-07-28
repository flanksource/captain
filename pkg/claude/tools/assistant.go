package tools

import (
	"strings"

	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
)

// MessagePreviewChars bounds the one-line preview a conversational row shows.
//
// It is a fixed character budget on purpose. These rows used to ask for
// `max-w-[tw-20ch]` -- the terminal width minus 20 -- and clicky applies that
// eagerly inside Text.Append, before any renderer runs. The body was therefore
// cut to a terminal width in HTML and markdown output too, and the remainder was
// destroyed rather than deferred: 74% of stored assistant bodies are longer than
// this preview. The full text now lives in Detail(), which every format can show.
const MessagePreviewChars = 120

// messagePreview collapses a body to a single line of at most
// MessagePreviewChars runes. Cutting on runes matters: bodies are prose, and a
// byte cut splits multi-byte characters.
func messagePreview(body string) string {
	body = compactText(body)
	runes := []rune(body)
	if len(runes) <= MessagePreviewChars {
		return body
	}
	return strings.TrimRight(string(runes[:MessagePreviewChars-1]), " ") + "…"
}

// messageDetail is the full body, for the formats that can render one.
func messageDetail(body string) api.Textable {
	if body == "" {
		return nil
	}
	return api.NewCode(body, "markdown")
}

// AssistantTool surfaces an assistant text turn (codex agent_message,
// claude assistant content text) as a row in the history output.
type AssistantTool struct{ BaseTool }

func (t *AssistantTool) Name() string        { return "Assistant" }
func (t *AssistantTool) Category() string    { return "chat" }
func (t *AssistantTool) FilePath() string    { return "" }
func (t *AssistantTool) ExtractPath() string { return "" }

func (t *AssistantTool) Pretty() api.Text {
	icon := icons.Icon{Unicode: "🤖", Iconify: "mdi:robot", Style: "muted"}
	text := t.header(icon, "assistant", "text-blue-500 font-medium")
	if body := t.Str("text"); body != "" {
		text = text.Append(" "+messagePreview(body), "text-muted")
	}
	return text
}

func (t *AssistantTool) Detail() api.Textable {
	if denied := t.BaseTool.Detail(); denied != nil {
		return denied
	}
	return messageDetail(t.Str("text"))
}

// ReasoningTool surfaces a reasoning/thinking summary (codex reasoning event,
// claude thinking blocks) so it shows up alongside other history rows.
type ReasoningTool struct{ BaseTool }

func (t *ReasoningTool) Name() string        { return "Reasoning" }
func (t *ReasoningTool) Category() string    { return "chat" }
func (t *ReasoningTool) FilePath() string    { return "" }
func (t *ReasoningTool) ExtractPath() string { return "" }

func (t *ReasoningTool) Pretty() api.Text {
	icon := icons.Icon{Unicode: "💭", Iconify: "mdi:thought-bubble", Style: "muted"}
	text := t.header(icon, "reasoning", "text-purple-500 font-medium")
	if body := t.Str("text"); body != "" {
		text = text.Append(" "+messagePreview(body), "text-muted italic")
	}
	return text
}

func (t *ReasoningTool) Detail() api.Textable {
	if denied := t.BaseTool.Detail(); denied != nil {
		return denied
	}
	return messageDetail(t.Str("text"))
}
