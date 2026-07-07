package prompt

import (
	"testing"

	"github.com/flanksource/captain/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParse_Fallbacks pins that an object-form fallbacks: block in frontmatter is
// accepted by the strict (KnownFields) spec decode and folds into
// Spec.Model.Fallbacks with each fallback's own knobs.
func TestParse_Fallbacks(t *testing.T) {
	const src = "---\n" +
		"model: claude-sonnet-5\n" +
		"effort: high\n" +
		"fallbacks:\n" +
		"  - model: gpt-4o\n" +
		"    effort: medium\n" +
		"  - model: gemini-2.0-flash\n" +
		"    temperature: 0.2\n" +
		"---\n" +
		"Summarize the diff.\n"

	doc, err := Parse(src)
	require.NoError(t, err)

	assert.Equal(t, "claude-sonnet-5", doc.Spec.Model.Name)
	require.Len(t, doc.Spec.Model.Fallbacks, 2)
	assert.Equal(t, "gpt-4o", doc.Spec.Model.Fallbacks[0].Name)
	assert.Equal(t, api.EffortMedium, doc.Spec.Model.Fallbacks[0].Effort)
	assert.Equal(t, "gemini-2.0-flash", doc.Spec.Model.Fallbacks[1].Name)
	require.NotNil(t, doc.Spec.Model.Fallbacks[1].Temperature)
	assert.EqualValues(t, 0.2, *doc.Spec.Model.Fallbacks[1].Temperature)
}

// TestParse_FallbacksExpandsInCandidates confirms the parsed primary + fallbacks
// produce the ordered try-list used at provider construction.
func TestParse_FallbacksExpandsInCandidates(t *testing.T) {
	const src = "---\nmodel: claude-sonnet-5\nfallbacks:\n  - model: gpt-4o\n---\nHi.\n"
	doc, err := Parse(src)
	require.NoError(t, err)

	candidates := doc.Spec.Model.Candidates()
	require.Len(t, candidates, 2)
	assert.Equal(t, "claude-sonnet-5", candidates[0].Name)
	assert.Equal(t, "gpt-4o", candidates[1].Name)
}
