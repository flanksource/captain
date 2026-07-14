package ai

import (
	"encoding/json"
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
