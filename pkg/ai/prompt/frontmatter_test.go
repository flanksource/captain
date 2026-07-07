package prompt

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// templatedSchemaPrompt declares an output.schema whose groups array carries a
// static minItems and a maxItems guarded by {{#if limit}} and interpolated from
// the render data — the exact shape gavel's commit-grouping prompt uses to cap the
// number of groups at runtime.
const templatedSchemaPrompt = "---\n" +
	"output:\n" +
	"  schema:\n" +
	"    type: object\n" +
	"    required: [groups]\n" +
	"    properties:\n" +
	"      groups:\n" +
	"        type: array\n" +
	"        minItems: 1\n" +
	"{{#if limit}}\n" +
	"        maxItems: {{limit}}\n" +
	"{{/if}}\n" +
	"        items:\n" +
	"          type: object\n" +
	"---\n" +
	"Group the files (limit {{limit}}).\n"

func groupsNode(t *testing.T, schemaJSON json.RawMessage) map[string]any {
	t.Helper()
	require.NotEmpty(t, schemaJSON, "frontmatter output.schema must be carried through")
	var schema map[string]any
	require.NoError(t, json.Unmarshal(schemaJSON, &schema))
	props, ok := schema["properties"].(map[string]any)
	require.True(t, ok, "schema has a properties object")
	groups, ok := props["groups"].(map[string]any)
	require.True(t, ok, "schema has a groups array")
	return groups
}

func TestRenderFrontmatter_InterpolatesOutputSchemaCap(t *testing.T) {
	req, _, err := Load(templatedSchemaPrompt).Render(map[string]any{"limit": 3}, nil)
	require.NoError(t, err)
	assert.Contains(t, req.Prompt.User, "limit 3", "body is still templated normally")

	groups := groupsNode(t, req.Prompt.SchemaJSON)
	assert.EqualValues(t, 3, groups["maxItems"], "maxItems interpolated from render data")
	assert.EqualValues(t, 1, groups["minItems"], "static minItems preserved")
}

func TestRenderFrontmatter_GuardOmitsCapWhenFalsy(t *testing.T) {
	req, _, err := Load(templatedSchemaPrompt).Render(map[string]any{"limit": 0}, nil)
	require.NoError(t, err)

	groups := groupsNode(t, req.Prompt.SchemaJSON)
	_, hasMax := groups["maxItems"]
	assert.False(t, hasMax, "{{#if limit}} omits maxItems when limit is 0")
	assert.EqualValues(t, 1, groups["minItems"], "minItems still present")
}

func TestRenderFrontmatter_NoTemplateIsInert(t *testing.T) {
	// A frontmatter with no {{ }} is returned byte-for-byte (no raymond pass), and a
	// literal {{limit}} in the BODY still renders — proving only the frontmatter is
	// pre-rendered.
	const static = "---\nmodel: claude-sonnet-4-6\n---\nEcho {{limit}}.\n"
	req, _, err := Load(static).Render(map[string]any{"limit": 5}, nil)
	require.NoError(t, err)
	assert.Contains(t, req.Prompt.User, "Echo 5.")
}
