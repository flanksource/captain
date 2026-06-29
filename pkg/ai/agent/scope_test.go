package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseScope(t *testing.T) {
	cases := map[string]struct {
		want    Scope
		wantErr bool
	}{
		"":         {ScopeAll, false},
		"all":      {ScopeAll, false},
		"changed":  {ScopeChanged, false},
		"sideways": {"", true},
	}
	for in, exp := range cases {
		got, err := ParseScope(in)
		if exp.wantErr {
			require.Error(t, err, "ParseScope(%q)", in)
			assert.Contains(t, err.Error(), ScopeList(), "error should name the valid set")
			continue
		}
		require.NoError(t, err, "ParseScope(%q)", in)
		assert.Equal(t, exp.want, got)
	}
}

func TestScopeValidAndList(t *testing.T) {
	assert.Equal(t, []Scope{ScopeAll, ScopeChanged}, AllScopes())
	assert.Equal(t, "all, changed", ScopeList())
	assert.True(t, ScopeAll.Valid())
	assert.True(t, ScopeChanged.Valid())
	assert.False(t, Scope("sideways").Valid())
}
