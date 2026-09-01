package prompt

import (
	"io/fs"
	"testing"

	"github.com/flanksource/captain/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Parse decodes spec-native frontmatter into Spec, leaves the body unrendered,
// and keeps dotprompt-only keys (config) in the raw frontmatter for round-trip.
func TestParse_SpecAndFrontmatter(t *testing.T) {
	src, err := fs.ReadFile(library, "testdata/options.prompt")
	require.NoError(t, err)

	doc, err := Parse(string(src))
	require.NoError(t, err)

	require.NotNil(t, doc.Spec.Sandbox)
	assert.Equal(t, api.PermissionAcceptEdits, doc.Spec.Permissions.Mode)
	assert.Equal(t, 3, doc.Spec.Budget.MaxTurns)

	// The body is left unrendered — handlebars markers are intact.
	assert.Contains(t, doc.Body, `{{role "user"}}`)
	assert.Contains(t, doc.Body, "{{target}}")

	// The dotprompt-only config: block is not a spec field but survives in the
	// raw frontmatter so String can reserialize it losslessly.
	assert.Contains(t, doc.Frontmatter, "config")
	assert.Contains(t, doc.Frontmatter, "permissions")
}

// String reserializes to a document Parse reads back, and an unmodeled nested
// block (output.schema) is not dropped when only spec keys are edited.
func TestDocument_StringRoundTrip(t *testing.T) {
	src := "---\n" +
		"model: claude-sonnet-4-6\n" +
		"output:\n" +
		"  schema:\n" +
		"    type: object\n" +
		"    properties:\n" +
		"      title:\n" +
		"        type: string\n" +
		"sandbox:\n" +
		"  mode: native\n" +
		"permissions:\n" +
		"  mode: acceptEdits\n" +
		"---\n" +
		"{{role \"user\"}}\nWrite a title for {{topic}}.\n"

	doc, err := Parse(src)
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-6", doc.Spec.Model.Name)
	require.NotNil(t, doc.Spec.Sandbox)
	assert.Equal(t, api.PermissionAcceptEdits, doc.Spec.Permissions.Mode)

	out, err := doc.String()
	require.NoError(t, err)
	assert.Contains(t, out, "schema", "the unmodeled output.schema block must survive serialization")
	assert.Contains(t, out, "title")

	rt, err := Parse(out)
	require.NoError(t, err)
	assert.Equal(t, doc.Spec.Model.Name, rt.Spec.Model.Name)
	require.NotNil(t, rt.Spec.Sandbox)
	assert.Equal(t, doc.Spec.Permissions.Mode, rt.Spec.Permissions.Mode)
	assert.Equal(t, doc.Body, rt.Body)
	assert.Contains(t, rt.Frontmatter, "output")
}

// A body-only prompt (no frontmatter markers) parses to a zero spec and
// round-trips byte-for-byte.
func TestParse_BodyOnly(t *testing.T) {
	src := "You are a strict reviewer. Evaluate {{diff}}.\n"

	doc, err := Parse(src)
	require.NoError(t, err)
	assert.Nil(t, doc.Frontmatter)
	assert.Equal(t, src, doc.Body)
	assert.Equal(t, api.Spec{}, doc.Spec)

	out, err := doc.String()
	require.NoError(t, err)
	assert.Equal(t, src, out)
}

// Malformed YAML frontmatter fails loud rather than being swallowed like the
// upstream dotprompt ParseDocument.
func TestParse_MalformedFrontmatter(t *testing.T) {
	_, err := Parse("---\nmodel: [unterminated\n---\nbody\n")
	require.Error(t, err)
}

// A top-level key the spec does not model fails loud (KnownFields), matching
// how Render rejects unknown spec-native keys.
func TestParse_UnknownSpecKey(t *testing.T) {
	_, err := Parse("---\nbogusKey: nope\n---\nbody\n")
	require.Error(t, err)
}
