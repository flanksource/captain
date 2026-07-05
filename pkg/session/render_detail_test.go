package session

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/claude/tools"
	"github.com/stretchr/testify/assert"
)

func TestRawSection_RendersJSONCodeBlock(t *testing.T) {
	raw := json.RawMessage(`{"type":"assistant","uuid":"x"}`)
	base := &tools.BaseTool{RawEntry: raw}

	section, ok := rawSection(base)
	assert.True(t, ok)
	rendered := section.String()

	// The label is present.
	assert.Contains(t, rendered, "Raw")
	// The JSON content survives indentation (CodeBlock pretty-prints; we
	// pre-indent before passing in).
	assert.Contains(t, rendered, `"type": "assistant"`)
	assert.Contains(t, rendered, `"uuid": "x"`)
}

func TestRawSection_HandlesInvalidJSON(t *testing.T) {
	// Even when json.Indent fails (malformed input), rawSection should still
	// emit the body verbatim instead of dropping it.
	raw := json.RawMessage(`not actually json`)
	base := &tools.BaseTool{RawEntry: raw}

	section, ok := rawSection(base)
	assert.True(t, ok)
	rendered := section.String()
	assert.Contains(t, rendered, "Raw")
	assert.Contains(t, rendered, "not actually json")
}

func TestRawSection_EmptyEntryReturnsFalse(t *testing.T) {
	_, ok := rawSection(&tools.BaseTool{})
	assert.False(t, ok)
	_, ok = rawSection(nil)
	assert.False(t, ok)
}

func TestRawSection_PrettyPrintsIndented(t *testing.T) {
	// Single-line raw should be expanded into multi-line indented JSON.
	raw := json.RawMessage(`{"a":1,"b":{"c":2}}`)
	section, ok := rawSection(&tools.BaseTool{RawEntry: raw})
	assert.True(t, ok)
	rendered := section.String()
	assert.True(t, strings.Contains(rendered, "\n"), "expected multi-line indented JSON, got %q", rendered)
}
