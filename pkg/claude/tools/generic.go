package tools

import (
	"bytes"
	"strings"

	"github.com/segmentio/encoding/json"

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
	if preview := genericPreview(t.Input); preview != "" {
		text = text.Append(" " + preview)
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
	if len(t.Input) == 0 {
		return nil
	}
	b, err := encodeJSON(t.Input, true)
	if err != nil {
		return nil
	}
	return api.NewCode(string(b), "json")
}

// genericPreview renders an unmapped tool's input as a one-line JSON preview.
//
// String values are whitespace-collapsed and HTML escaping is switched off
// before marshalling. Marshalling a raw input map encodes every newline as the
// two characters backslash-n and every "<" as backslash-u-0-0-3-c, so a value
// holding a multi-line body -- a system prompt, a heredoc -- renders as a wall
// of escape sequences that consumes the whole preview budget before any of the
// body is reached.
func genericPreview(input map[string]any) string {
	if len(input) == 0 {
		return ""
	}
	b, err := encodeJSON(compactStrings(input), false)
	if err != nil {
		return ""
	}
	return messagePreview(string(b))
}

// compactStrings collapses whitespace in every string the value tree holds.
func compactStrings(v any) any {
	switch v := v.(type) {
	case string:
		return compactText(v)
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, val := range v {
			out[k] = compactStrings(val)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, val := range v {
			out[i] = compactStrings(val)
		}
		return out
	default:
		return v
	}
}

// encodeJSON marshals without the HTML escaping the package applies by
// default, so "<", ">" and "&" reach rendered output as themselves.
func encodeJSON(v any, indent bool) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
