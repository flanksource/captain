package tools

import (
	"github.com/segmentio/encoding/json"
	"strings"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
)

var toolIcons = map[string]icons.Icon{
	"Skill": icons.Info,
}

var toolColors = map[string]string{
	"Skill": "text-teal-400 font-medium",
}

type GenericTool struct{ BaseTool }

func (t *GenericTool) Name() string        { return t.RawTool }
func (t *GenericTool) Category() string    { return "" }
func (t *GenericTool) FilePath() string    { return "" }
func (t *GenericTool) ExtractPath() string { return "" }

func (t *GenericTool) Pretty() api.Text {
	icon := toolIcons[t.RawTool]
	color, ok := toolColors[t.RawTool]
	if !ok {
		color = "text-blue-400 font-medium"
	}
	text := t.header(icon, strings.ToLower(t.RawTool), color)
	if b, err := json.Marshal(t.Input); err == nil {
		text = text.Append(" " + messagePreview(string(b)))
	}
	return text
}

func (t *GenericTool) Detail() api.Textable {
	if t.Denied && t.DeniedReason != "" {
		text := clicky.Text("").Append("User: ", "font-bold text-red-500").Append(t.DeniedReason, "")
		return &text
	}
	// The preview is a truncated one-liner; the full input belongs somewhere a
	// non-terminal format can still reach it.
	b, err := json.Marshal(t.Input)
	if err != nil || len(t.Input) == 0 {
		return nil
	}
	return api.NewCode(string(b), "json")
}
