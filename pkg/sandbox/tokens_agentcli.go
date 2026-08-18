package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flanksource/captain/pkg/agentcreds"
)

// Claude and Codex are acquired through the same three steps — read the host
// login, strip the refresh token, drop the result into a private config
// directory — so they share one implementation and differ only by the table
// below.
//
// Unlike the cloud providers, these do not export a credential *value*; they
// redirect the CLI's whole config directory (CLAUDE_CONFIG_DIR / CODEX_HOME) at
// a redacted copy. That is what lets srt.go add the host's real credential
// files to DenyRead: the sandboxed CLI authenticates from the copy and can no
// longer reach the original, which still holds the refresh token.

// NewAgentCredentialReader resolves the host reader. A variable for the same
// reason adapter.NewSRTRuntime is one: it lets the acquisition path be tested
// against fixture documents rather than requiring a real Keychain login on
// whatever machine runs the suite.
var NewAgentCredentialReader = agentcreds.OSReader

type agentCLIProvider struct {
	provider agentcreds.Provider
	// dirName is the subdirectory of credDir that becomes the CLI's config home.
	dirName string
	// homeEnv is the variable pointing the CLI at that directory.
	homeEnv string
	// seedFiles are host config files copied in alongside the credential.
	// Redirecting CODEX_HOME moves the entire codex home, so without
	// config.toml the sandboxed CLI silently loses the user's model and
	// provider configuration.
	seedFiles []string
}

func agentCLIProviders() map[agentcreds.Provider]agentCLIProvider {
	return map[agentcreds.Provider]agentCLIProvider{
		agentcreds.ProviderClaude: {
			provider: agentcreds.ProviderClaude,
			dirName:  "claude",
			homeEnv:  "CLAUDE_CONFIG_DIR",
		},
		agentcreds.ProviderCodex: {
			provider:  agentcreds.ProviderCodex,
			dirName:   "codex",
			homeEnv:   "CODEX_HOME",
			seedFiles: []string{"config.toml"},
		},
	}
}

func acquireClaudeToken(ctx context.Context, _ ClaudeTokenConfig, credDir string) (*TokenResult, error) {
	return acquireAgentCLIToken(ctx, agentcreds.ProviderClaude, credDir)
}

func acquireCodexToken(ctx context.Context, _ CodexTokenConfig, credDir string) (*TokenResult, error) {
	return acquireAgentCLIToken(ctx, agentcreds.ProviderCodex, credDir)
}

func acquireAgentCLIToken(ctx context.Context, provider agentcreds.Provider, credDir string) (*TokenResult, error) {
	spec, ok := agentCLIProviders()[provider]
	if !ok {
		return nil, fmt.Errorf("no agent CLI provider named %q", provider)
	}
	reader, err := NewAgentCredentialReader()
	if err != nil {
		return nil, err
	}
	credential, err := reader.Read(ctx, provider)
	if err != nil {
		return nil, err
	}

	configDir := filepath.Join(credDir, spec.dirName)
	if err := atomicWriteFile(filepath.Join(configDir, credential.RelPath()), credential.Payload, 0o600); err != nil {
		return nil, fmt.Errorf("write %s credentials: %w", provider, err)
	}
	if err := seedAgentCLIConfig(reader, spec, configDir); err != nil {
		return nil, err
	}

	return &TokenResult{
		Provider:   string(provider),
		EnvVars:    map[string]string{spec.homeEnv: configDir},
		WritePaths: []string{configDir},
		Expiry:     credential.ExpiresAt,
	}, nil
}

// seedAgentCLIConfig copies the host's non-credential configuration into the
// redirected config directory. A missing source file is fine — the user simply
// has no such config — but an unreadable one is an error rather than a silently
// differently-configured CLI.
func seedAgentCLIConfig(reader agentcreds.Reader, spec agentCLIProvider, configDir string) error {
	for _, name := range spec.seedFiles {
		source := filepath.Join(reader.Home, "."+spec.dirName, name)
		data, err := os.ReadFile(source)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read %s config %s: %w", spec.provider, source, err)
		}
		if err := atomicWriteFile(filepath.Join(configDir, name), data, 0o600); err != nil {
			return fmt.Errorf("seed %s config %s: %w", spec.provider, name, err)
		}
	}
	return nil
}
