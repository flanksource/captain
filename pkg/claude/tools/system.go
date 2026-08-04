package tools

import (
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
)

// SystemTool surfaces a system-role message -- the Codex session prompt, plugin
// and skill instruction blocks -- as a history row, the same way UserTool and
// AssistantTool surface the other two conversational roles.
//
// Without it the row falls through to GenericTool, which marshals the whole
// input map. A system prompt carries hundreds of newlines, so the preview
// budget is spent on JSON escape sequences and the reader never sees the
// prompt at all.
type SystemTool struct{ BaseTool }

func (t *SystemTool) Name() string        { return "System" }
func (t *SystemTool) Category() string    { return "chat" }
func (t *SystemTool) FilePath() string    { return "" }
func (t *SystemTool) ExtractPath() string { return "" }

func (t *SystemTool) Pretty() api.Text {
	icon := icons.Icon{Unicode: "⚙️", Iconify: "mdi:cog", Style: "muted"}
	text := t.header(icon, "system", "text-slate-500 font-medium")
	if body := t.Str("text"); body != "" {
		text = text.Append(" "+messagePreview(body), "text-muted")
	}
	return text
}

func (t *SystemTool) Detail() api.Textable {
	if denied := t.BaseTool.Detail(); denied != nil {
		return denied
	}
	return messageDetail(t.Str("text"))
}
