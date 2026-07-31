package claudeagent

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
)

//go:embed agent.ts
var agentTS string

//go:embed protocol.ts
var protocolTS string

//go:embed package.json
var agentPackageJSON string

// prepareAgentDir materialises the embedded agent.ts and package.json into a
// stable per-user cache directory so the supervised tsx process and `npm
// install` have a real working tree. The directory is reused across runs;
// writeIfChanged avoids spurious rewrites (and the npm reinstall they imply).
func prepareAgentDir() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user cache dir: %w", err)
	}

	agentDir := filepath.Join(cacheDir, "captain", "claude-agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create agent dir %s: %w", agentDir, err)
	}

	if err := writeIfChanged(filepath.Join(agentDir, "agent.ts"), agentTS); err != nil {
		return "", err
	}
	if err := writeIfChanged(filepath.Join(agentDir, "protocol.ts"), protocolTS); err != nil {
		return "", err
	}
	if err := writeIfChanged(filepath.Join(agentDir, "package.json"), agentPackageJSON); err != nil {
		return "", err
	}

	return agentDir, nil
}

// writeIfChanged writes content to path only when it differs from what is
// already there, so an unchanged embed does not bump the mtime and trigger an
// unnecessary dependency reinstall.
func writeIfChanged(path, content string) error {
	existing, err := os.ReadFile(path)
	if err == nil && contentHash(existing) == contentHash([]byte(content)) {
		return nil
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}

func contentHash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// ensureDependencies installs the pinned SDK when it is missing or its version
// differs. It fails loudly when npm is unavailable because the provider cannot
// run without the exact bridge contract declared in package.json.
func ensureDependencies(agentDir string) error {
	requiredVersion, err := requiredSDKVersion()
	if err != nil {
		return err
	}
	sdkPackage := filepath.Join(agentDir, "node_modules", "@anthropic-ai", "claude-agent-sdk", "package.json")
	if data, err := os.ReadFile(sdkPackage); err == nil {
		var installed struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(data, &installed) == nil && installed.Version == requiredVersion {
			return nil
		}
	}
	npmPath, err := exec.LookPath("npm")
	if err != nil {
		return fmt.Errorf("npm not found in PATH (required to install the Claude Agent SDK): %w", err)
	}

	cmd := exec.Command(npmPath, "install")
	cmd.Dir = agentDir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("npm install in %s failed: %w", agentDir, err)
	}
	return nil
}

func requiredSDKVersion() (string, error) {
	var manifest struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal([]byte(agentPackageJSON), &manifest); err != nil {
		return "", fmt.Errorf("parse embedded Claude Agent package manifest: %w", err)
	}
	version := strings.TrimSpace(manifest.Dependencies["@anthropic-ai/claude-agent-sdk"])
	if version == "" {
		return "", fmt.Errorf("embedded Claude Agent package manifest does not pin @anthropic-ai/claude-agent-sdk")
	}
	return version, nil
}

// findTsx resolves the tsx runner, preferring the version installed into the
// agent dir's node_modules over a global one.
func findTsx(agentDir string) (string, error) {
	localTsx := filepath.Join(agentDir, "node_modules", ".bin", "tsx")
	if _, err := os.Stat(localTsx); err == nil {
		return localTsx, nil
	}
	if path, err := exec.LookPath("tsx"); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("tsx not found; install with: npm install -g tsx")
}

// nestingEnvOverrides returns environment overrides that blank out the
// CLAUDECODE* / CLAUDE_CODE_ENTRYPOINT markers the SDK uses to detect a nested
// session. clicky's exec always inherits os.Environ(), so these can't be
// dropped from the slice — blanking them (a falsy value the SDK ignores) is the
// equivalent strip. agent.ts also deletes them at startup as the authoritative
// guard.
func nestingEnvOverrides(environ []string) map[string]string {
	overrides := map[string]string{}
	for _, e := range environ {
		key, _, _ := strings.Cut(e, "=")
		if strings.HasPrefix(key, "CLAUDECODE") || key == "CLAUDE_CODE_ENTRYPOINT" {
			overrides[key] = ""
		}
	}
	return overrides
}

// aliasModel is retained as the local model renderer for the agent.ts bridge.
// It accepts legacy backend-prefixed aliases as input compatibility, but returns
// the exact Claude model ID the SDK should receive.
func aliasModel(model string) string {
	m := strings.TrimSpace(model)
	if m == "" || m == "claude" {
		return "claude-sonnet-5"
	}
	return ai.NormalizeModelForBackend(ai.BackendClaudeAgent, m)
}
