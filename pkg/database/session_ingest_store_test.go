package database

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizePostgresJSON(t *testing.T) {
	input := []byte(`{"nul":"before\u0000after","literal":"before\\u0000after","nested":["\u0000"]}`)
	got, err := sanitizePostgresJSON(input)
	require.NoError(t, err)
	assert.Equal(t,
		`{"nul":"before\ufffdafter","literal":"before\\u0000after","nested":["\ufffd"]}`,
		string(got),
	)
}

func TestSanitizePostgresJSONPreservesCleanBytes(t *testing.T) {
	input := []byte(`{ "number": 9223372036854775807, "text": "clean" }`)
	got, err := sanitizePostgresJSON(input)
	require.NoError(t, err)
	assert.Equal(t, input, got)
}

func TestSanitizePostgresJSONRejectsInvalidInput(t *testing.T) {
	_, err := sanitizePostgresJSON([]byte(`{"broken":`))
	require.Error(t, err)
}

func TestJSONBValueSanitizesNullCharacters(t *testing.T) {
	got := jsonbValue(map[string]any{"value": "before\x00after"})
	assert.JSONEq(t, `{"value":"before\ufffdafter"}`, got.(string))
}
