// Specs for CredentialFor: how each provider's key reaches its model API through the egress
// gateway, and which provider and auth pairs it doesn't know.
package substrate_test

import (
	"testing"

	"github.com/flanksource/captain/pkg/substrate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCredentialForNamesWhereEachKeyGoes(t *testing.T) {
	for _, tc := range []struct {
		provider, auth string
		want           substrate.Credential
	}{
		{"anthropic", "api-key", substrate.Credential{Host: "api.anthropic.com", Header: "x-api-key", Env: "ANTHROPIC_API_KEY"}},
		{"anthropic", "oauth-token", substrate.Credential{Host: "api.anthropic.com", Header: "Authorization", Prefix: "Bearer ", Env: "CLAUDE_CODE_OAUTH_TOKEN"}},
		{"openai", "api-key", substrate.Credential{Host: "api.openai.com", Header: "Authorization", Prefix: "Bearer ", Env: "OPENAI_API_KEY"}},
	} {
		got, err := substrate.CredentialFor(tc.provider, tc.auth)
		require.NoError(t, err, "%s %s", tc.provider, tc.auth)
		assert.Equal(t, tc.want, got, "%s %s", tc.provider, tc.auth)
	}
}

func TestCredentialForRefusesWhatItDoesNotKnow(t *testing.T) {
	_, err := substrate.CredentialFor("gemini", "api-key")
	require.EqualError(t, err, `substrate injects no gemini api-key credential; known: anthropic api-key, anthropic oauth-token, openai api-key`)
}
