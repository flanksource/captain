package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// systemPrompt is a miniature of a real Codex system row: a multi-line body
// holding an XML-ish tag, the two shapes that JSON marshalling mangles.
const systemPrompt = "<plugins_instructions>\n## Plugins\nA plugin is a local bundle of skills.\n"

// escapedNewline and escapedLT are what json.Marshal emits for a real newline
// and a real "<". Both are two- and six-character sequences in the rendered
// output, not control characters, which is exactly why they are unreadable.
const (
	escapedNewline = "\\n"
	escapedLT      = "\\u003c"
)

func TestNewTool_SystemRoleDispatchesToSystemTool(t *testing.T) {
	tool := NewTool(BaseTool{RawTool: "System", Input: map[string]any{"text": systemPrompt}})

	require.IsType(t, &SystemTool{}, tool)
	assert.Equal(t, "System", tool.Name())
}

func TestSystemTool_PrettyShowsBodyWithoutJSONEscapes(t *testing.T) {
	pretty := (&SystemTool{BaseTool: BaseTool{
		RawTool: "System",
		Input:   map[string]any{"text": systemPrompt},
	}}).Pretty().String()

	assert.Contains(t, pretty, "system")
	assert.Contains(t, pretty, "<plugins_instructions> ## Plugins A plugin is a local bundle of skills.")
	assert.NotContains(t, pretty, escapedNewline, "newlines must be collapsed, not JSON-escaped")
	assert.NotContains(t, pretty, escapedLT, "angle brackets must not be HTML-escaped")
	assert.NotContains(t, pretty, `{"text"`, "the row must show the body, not its JSON envelope")
}

func TestSystemTool_DetailKeepsFullBody(t *testing.T) {
	detail := (&SystemTool{BaseTool: BaseTool{
		RawTool: "System",
		Input:   map[string]any{"text": systemPrompt},
	}}).Detail()

	require.NotNil(t, detail)
	assert.Contains(t, detail.String(), systemPrompt)
}

func TestGenericTool_PreviewCollapsesNewlinesAndKeepsAngleBrackets(t *testing.T) {
	tool := NewTool(BaseTool{RawTool: "write_stdin", Input: map[string]any{
		"chars": "echo <hi>\nexit\n",
		"tags":  []any{"a\nb"},
	}})
	require.IsType(t, &GenericTool{}, tool)

	pretty := tool.Pretty().String()
	assert.Contains(t, pretty, "write_stdin")
	assert.Contains(t, pretty, "echo <hi> exit")
	assert.Contains(t, pretty, `"a b"`, "nested string values are collapsed too")
	assert.NotContains(t, pretty, escapedNewline)
	assert.NotContains(t, pretty, escapedLT)
}

func TestGenericTool_DetailIsReadableJSON(t *testing.T) {
	detail := (&GenericTool{BaseTool: BaseTool{
		RawTool: "write_stdin",
		Input:   map[string]any{"chars": "echo <hi>"},
	}}).Detail()

	require.NotNil(t, detail)
	assert.Contains(t, detail.String(), "echo <hi>")
	assert.NotContains(t, detail.String(), escapedLT)
}
