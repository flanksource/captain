// Where each provider's key goes on its way to the model API: the host, the header the egress
// gateway replaces, and the variable the agent's client reads a placeholder from.
package substrate

import (
	"fmt"
	"strings"
)

type credentialKind struct {
	provider, auth string
	credential     Credential
}

var credentialKinds = []credentialKind{
	{"anthropic", "api-key", Credential{Host: "api.anthropic.com", Header: "x-api-key", Env: "ANTHROPIC_API_KEY"}},
	{"anthropic", "oauth-token", Credential{Host: "api.anthropic.com", Header: "Authorization", Prefix: "Bearer ", Env: "CLAUDE_CODE_OAUTH_TOKEN"}},
	{"openai", "api-key", Credential{Host: "api.openai.com", Header: "Authorization", Prefix: "Bearer ", Env: "OPENAI_API_KEY"}},
}

// CredentialFor is how provider's key of kind auth reaches its model API, without the URI that
// names the key, which is the caller's.
func CredentialFor(provider, auth string) (Credential, error) {
	known := make([]string, len(credentialKinds))
	for i, kind := range credentialKinds {
		if kind.provider == provider && kind.auth == auth {
			return kind.credential, nil
		}
		known[i] = kind.provider + " " + kind.auth
	}
	return Credential{}, fmt.Errorf("substrate injects no %s %s credential; known: %s", provider, auth, strings.Join(known, ", "))
}
