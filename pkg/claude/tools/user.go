package tools

import (
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
)

type UserTool struct{ BaseTool }

func (t *UserTool) Name() string        { return "User" }
func (t *UserTool) Category() string    { return "" }
func (t *UserTool) FilePath() string    { return "" }
func (t *UserTool) ExtractPath() string { return "" }
func (t *UserTool) Detail() api.Textable { return nil }

func (t *UserTool) Pretty() api.Text {
	icon := icons.Icon{Unicode: "💬", Iconify: "mdi:account", Style: "muted"}
	text := t.header(icon, "user", "text-amber-400")
	return text.Append(" "+t.Str("text"), "max-w-[tw-20ch]")
}
