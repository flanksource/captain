package ai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateStructuredJSON(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","required":["pass"],"properties":{"pass":{"type":"boolean"}}}`)

	violations, err := ValidateStructuredJSON(schema, `{"pass":true}`)
	require.NoError(t, err)
	assert.Empty(t, violations)

	violations, err = ValidateStructuredJSON(schema, `{"pass":"yes"}`)
	require.NoError(t, err)
	assert.Contains(t, violations, "boolean")

	violations, err = ValidateStructuredJSON(schema, "")
	require.NoError(t, err)
	assert.Equal(t, "response carried no JSON to validate", violations)
}

func TestValidateStructuredJSONReportsMalformedResponsePreview(t *testing.T) {
	schema := json.RawMessage(`{"type":"object"}`)
	response := strings.Repeat("A", 500) + "TAIL-NOT-IN-PREVIEW"

	_, err := ValidateStructuredJSON(schema, response)
	require.ErrorIs(t, err, ErrSchemaValidation)
	assert.Contains(t, err.Error(), "validation could not run")
	assert.Contains(t, err.Error(), response[:500])
	assert.NotContains(t, err.Error(), "TAIL-NOT-IN-PREVIEW")

	t.Run("includes short responses completely", func(t *testing.T) {
		response := "not JSON"
		_, err := ValidateStructuredJSON(schema, response)
		require.ErrorIs(t, err, ErrSchemaValidation)
		assert.Contains(t, err.Error(), response)
	})
}
