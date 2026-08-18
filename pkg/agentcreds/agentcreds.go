// Package agentcreds reads the subscription logins the agent CLIs keep on the
// host, strips the refresh token, and reports when the remainder expires.
//
// Captain has always detected these logins by existence alone
// (pkg/ai/adapters.go stats ~/.claude/.credentials.json and ~/.codex/auth.json
// without opening them). This package is the first thing that reads them, and
// it exists so a sandbox can be handed a credential that authenticates but
// cannot mint a new one: the refresh token stays on the host.
//
// That deliberately makes the redacted credential short-lived, which is why
// ExpiresAt is part of the contract rather than an afterthought — every
// consumer has to republish before it lapses.
package agentcreds

import (
	"fmt"
	"strings"
	"time"
)

// Provider names a credential source. The values are the tokens used in
// configuration (`tokens: {claude: {}}`) and as CLI arguments.
type Provider string

const (
	ProviderClaude Provider = "claude"
	ProviderCodex  Provider = "codex"
)

// Providers returns every supported provider in a stable order.
func Providers() []Provider { return []Provider{ProviderClaude, ProviderCodex} }

// ParseProvider resolves a user-supplied provider name.
func ParseProvider(name string) (Provider, error) {
	switch p := Provider(strings.ToLower(strings.TrimSpace(name))); p {
	case ProviderClaude, ProviderCodex:
		return p, nil
	default:
		return "", fmt.Errorf("unknown credential provider %q (want one of: claude, codex)", name)
	}
}

// Credential is one provider's redacted login, ready to be written where the
// CLI expects to find it.
type Credential struct {
	Provider Provider
	// Filename is the base name the CLI reads, and doubles as the key this
	// credential occupies in a Kubernetes Secret or a published directory.
	Filename string
	// Payload is the redacted JSON document, exactly as it should land on disk.
	Payload []byte
	// ExpiresAt is when Payload stops authenticating. Never zero: a credential
	// whose expiry cannot be determined is an error at read time, because a
	// consumer that cannot schedule a republish would silently serve a dead
	// token.
	ExpiresAt time.Time
}

// Expired reports whether the credential has already lapsed at now.
func (c Credential) Expired(now time.Time) bool { return !now.Before(c.ExpiresAt) }

// The file names each CLI reads. RelPath is where the file sits under the
// CLI's own config directory (CLAUDE_CONFIG_DIR / CODEX_HOME).
const (
	ClaudeFilename = "claude.credentials.json"
	CodexFilename  = "codex.auth.json"

	// ClaudeRelPath is the name Claude Code reads inside CLAUDE_CONFIG_DIR.
	ClaudeRelPath = ".credentials.json"
	// CodexRelPath is the name codex reads inside CODEX_HOME.
	CodexRelPath = "auth.json"
)

// RelPath is where this credential must be written inside the provider's
// config directory for the CLI to find it.
func (c Credential) RelPath() string {
	if c.Provider == ProviderClaude {
		return ClaudeRelPath
	}
	return CodexRelPath
}
