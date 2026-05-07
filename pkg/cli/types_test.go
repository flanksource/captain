package cli

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScanResultRow_MarshalJSON_NoRawNoChange(t *testing.T) {
	row := ScanResultRow{Tool: "Bash", Category: "explore", Approved: "✓"}
	out, err := json.Marshal(row)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, "Bash", got["tool"])
	assert.Equal(t, "explore", got["category"])
	// raw key absent when Raw is empty
	_, hasRaw := got["raw"]
	assert.False(t, hasRaw)
}

func TestScanResultRow_MarshalJSON_MergesRawObject(t *testing.T) {
	raw := json.RawMessage(`{"session_id":"sess-1","error":"oops","extra":42}`)
	row := ScanResultRow{Tool: "ApiError", Category: "error", Raw: raw}

	out, err := json.Marshal(row)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	// Standard fields preserved
	assert.Equal(t, "ApiError", got["tool"])
	assert.Equal(t, "error", got["category"])
	// Raw fields merged at top level
	assert.Equal(t, "sess-1", got["session_id"])
	assert.Equal(t, "oops", got["error"])
	assert.EqualValues(t, 42, got["extra"])
}

func TestScanResultRow_MarshalJSON_RowFieldsWinOnConflict(t *testing.T) {
	// Raw has a "category" key — the row's category should NOT be overwritten.
	raw := json.RawMessage(`{"category":"hijacked","new_field":"x"}`)
	row := ScanResultRow{Tool: "Bash", Category: "explore", Raw: raw}

	out, err := json.Marshal(row)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, "explore", got["category"], "row.Category must take precedence over raw.category")
	assert.Equal(t, "x", got["new_field"])
}

func TestScanResultRow_MarshalJSON_NonObjectRawGoesUnderRawKey(t *testing.T) {
	// Raw isn't a JSON object (e.g. a string or array) — surface under "raw".
	raw := json.RawMessage(`"plain string"`)
	row := ScanResultRow{Tool: "Bash", Raw: raw}

	out, err := json.Marshal(row)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, "plain string", got["raw"])
}

func TestScanResultRowSingle_MarshalJSON_MergesRaw(t *testing.T) {
	raw := json.RawMessage(`{"uuid":"u1","timestamp":"2024-01-01T00:00:00Z"}`)
	row := ScanResultRowSingle{Tool: "Bash", Raw: raw}

	out, err := json.Marshal(row)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, "Bash", got["tool"])
	assert.Equal(t, "u1", got["uuid"])
	assert.Equal(t, "2024-01-01T00:00:00Z", got["timestamp"])
}
