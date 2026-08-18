package sandbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/agentcreds"
)

// The acquirers redirect each CLI's whole config directory at a redacted copy,
// so what these assert is the contract the CLI depends on: the credential is at
// the path it reads, the env var points there, and the refresh token is gone.

func jwtWithExp(instant time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	claims := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, instant.Unix())))
	return header + "." + claims + ".sig"
}

// stubHostLogins points the acquirers at fixture documents in a fake home,
// restoring the real reader when the test ends.
func stubHostLogins(t *testing.T, claudeExpiry, codexExpiry time.Time) string {
	t.Helper()
	home := t.TempDir()
	for path, content := range map[string]string{
		filepath.Join(home, ".claude", ".credentials.json"): fmt.Sprintf(
			`{"claudeAiOauth":{"accessToken":"claude-access","refreshToken":"claude-refresh","expiresAt":%d},"mcpOAuth":{"srv|abc":{"clientSecret":"mcp-secret"}}}`,
			claudeExpiry.UnixMilli()),
		filepath.Join(home, ".codex", "auth.json"): fmt.Sprintf(
			`{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":%q,"access_token":%q,"refresh_token":"codex-refresh","account_id":"acct"}}`,
			jwtWithExp(codexExpiry), jwtWithExp(codexExpiry)),
		// Seeded alongside the credential: redirecting CODEX_HOME moves the whole
		// codex home, so losing this would silently reconfigure the CLI.
		filepath.Join(home, ".codex", "config.toml"): "model = \"gpt-5.6-sol\"\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	original := NewAgentCredentialReader
	NewAgentCredentialReader = func() (agentcreds.Reader, error) {
		// No ReadKeychain, so the file path is used — the Linux/container shape.
		return agentcreds.Reader{Home: home, ReadFile: os.ReadFile, Now: time.Now}, nil
	}
	t.Cleanup(func() { NewAgentCredentialReader = original })
	return home
}

func TestAcquireClaudeTokenRedirectsTheConfigDirectory(t *testing.T) {
	expiry := time.Now().Add(2 * time.Hour).Truncate(time.Millisecond)
	stubHostLogins(t, expiry, time.Now().Add(48*time.Hour))
	credDir := t.TempDir()

	result, err := acquireClaudeToken(context.Background(), ClaudeTokenConfig{}, credDir)
	if err != nil {
		t.Fatal(err)
	}

	configDir := filepath.Join(credDir, "claude")
	if got := result.EnvVars["CLAUDE_CONFIG_DIR"]; got != configDir {
		t.Errorf("CLAUDE_CONFIG_DIR = %q, want %q", got, configDir)
	}
	if !result.Expiry.Equal(expiry.UTC()) {
		t.Errorf("Expiry = %v, want %v", result.Expiry, expiry.UTC())
	}

	// The path Claude Code actually reads inside CLAUDE_CONFIG_DIR.
	written, err := os.ReadFile(filepath.Join(configDir, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"claude-refresh", "mcp-secret"} {
		if strings.Contains(string(written), secret) {
			t.Errorf("%q survived redaction into the sandbox credential", secret)
		}
	}
	var document map[string]any
	if err := json.Unmarshal(written, &document); err != nil {
		t.Fatal(err)
	}
	if _, present := document["mcpOAuth"]; present {
		t.Error("mcpOAuth reached the sandbox")
	}
}

func TestAcquireCodexTokenSeedsConfigAlongsideTheCredential(t *testing.T) {
	stubHostLogins(t, time.Now().Add(2*time.Hour), time.Now().Add(48*time.Hour))
	credDir := t.TempDir()

	result, err := acquireCodexToken(context.Background(), CodexTokenConfig{}, credDir)
	if err != nil {
		t.Fatal(err)
	}

	configDir := filepath.Join(credDir, "codex")
	if got := result.EnvVars["CODEX_HOME"]; got != configDir {
		t.Errorf("CODEX_HOME = %q, want %q", got, configDir)
	}
	config, err := os.ReadFile(filepath.Join(configDir, "config.toml"))
	if err != nil {
		t.Fatalf("host config.toml was not seeded: %v", err)
	}
	if !strings.Contains(string(config), "gpt-5.6-sol") {
		t.Errorf("seeded config.toml = %q", config)
	}

	written, err := os.ReadFile(filepath.Join(configDir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(written), "codex-refresh") {
		t.Error("the refresh token reached the sandbox")
	}
	// Present but empty, not absent: codex-rs models refresh_token as a
	// non-optional String.
	var document struct {
		Tokens struct {
			RefreshToken *string `json:"refresh_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(written, &document); err != nil {
		t.Fatal(err)
	}
	if document.Tokens.RefreshToken == nil || *document.Tokens.RefreshToken != "" {
		t.Errorf("refresh_token = %v, want a present empty string", document.Tokens.RefreshToken)
	}
}

func TestAcquireWritesPrivateCredentialFiles(t *testing.T) {
	stubHostLogins(t, time.Now().Add(2*time.Hour), time.Now().Add(48*time.Hour))
	credDir := t.TempDir()

	if _, err := acquireClaudeToken(context.Background(), ClaudeTokenConfig{}, credDir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(credDir, "claude", ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("credential mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestTokenManagerAcquiresBothAgentLogins(t *testing.T) {
	// The manager is the seam the sandbox adapters call, and until this change it
	// had no callers at all — so this asserts the wiring, not just the acquirers.
	stubHostLogins(t, time.Now().Add(2*time.Hour), time.Now().Add(48*time.Hour))
	credDir := t.TempDir()

	manager := NewTokenManager(credDir)
	results, err := manager.Acquire(context.Background(), &TokensConfig{
		Claude: &ClaudeTokenConfig{},
		Codex:  &CodexTokenConfig{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("acquired %d results, want 2", len(results))
	}
	providers := map[string]bool{}
	for _, result := range results {
		providers[result.Provider] = true
		if result.Expiry.IsZero() {
			t.Errorf("%s reported no expiry, so a refresh cannot be scheduled", result.Provider)
		}
	}
	if !providers["claude"] || !providers["codex"] {
		t.Errorf("acquired providers = %v", providers)
	}
}

func TestAcquireFailsLoudlyWhenTheHostIsNotLoggedIn(t *testing.T) {
	original := NewAgentCredentialReader
	NewAgentCredentialReader = func() (agentcreds.Reader, error) {
		return agentcreds.Reader{Home: t.TempDir(), ReadFile: os.ReadFile, Now: time.Now}, nil
	}
	t.Cleanup(func() { NewAgentCredentialReader = original })

	_, err := acquireClaudeToken(context.Background(), ClaudeTokenConfig{}, t.TempDir())
	if err == nil {
		t.Fatal("a missing host login was accepted; the sandbox would start unauthenticated")
	}
	if !strings.Contains(err.Error(), "run `claude`") {
		t.Errorf("error does not name the remedy: %v", err)
	}
}
