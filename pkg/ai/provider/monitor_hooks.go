package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/claude"
)

// monitorHookTimeout is the settings.json timeout for the injected hooks; the
// receiver itself delivers-or-drops within 1s, this is only the safety bound.
const monitorHookTimeout = 10

// monitorHookEvents are the Claude Code lifecycle events captain subscribes to
// for session monitoring (matching `captain hook monitor install`).
var monitorHookEvents = []claude.HookEventType{
	claude.HookEventSessionStart,
	claude.HookEventUserPromptSubmit,
	claude.HookEventStop,
	claude.HookEventSubagentStop,
	claude.HookEventSessionEnd,
}

// captainBinary resolves the binary the injected hooks invoke. This package is
// also consumed as a library from other binaries (gavel, tests), where
// os.Executable is not captain; fall back to PATH, and report false when no
// captain exists — injection is then skipped and the user-level hook install
// plus the monitor's recon cover those sessions.
func captainBinary() (string, bool) {
	path, err := os.Executable()
	if err == nil && strings.HasPrefix(filepath.Base(path), "captain") {
		return path, true
	}
	if found, err := exec.LookPath("captain"); err == nil {
		return found, true
	}
	return "", false
}

// claudeMonitorSettings is the --settings document injected into
// captain-launched claude CLI sessions so they report lifecycle events even
// without the user-level hook install.
func claudeMonitorSettings(binary string) ([]byte, error) {
	command := fmt.Sprintf("%s hook monitor notify --provider claude", binary)
	hooks := claude.HooksConfig{Hooks: map[claude.HookEventType][]claude.HookMatcher{}}
	for _, event := range monitorHookEvents {
		hooks.Hooks[event] = []claude.HookMatcher{{
			Hooks: []claude.Hook{{Type: claude.HookTypeCommand, Command: command, Timeout: monitorHookTimeout}},
		}}
	}
	data, err := json.Marshal(hooks)
	if err != nil {
		return nil, fmt.Errorf("encode monitor hook settings: %w", err)
	}
	return data, nil
}

// writeClaudeSettings writes the composed native-sandbox and monitor-hook
// document to a temp file and returns its path with a cleanup.
func writeClaudeSettings(data []byte) (string, func(), error) {
	f, err := os.CreateTemp("", "captain-claude-hooks-*.json")
	if err != nil {
		return "", nil, fmt.Errorf("claude-cli: create monitor hooks settings file: %w", err)
	}
	path := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", nil, fmt.Errorf("claude-cli: write monitor hooks settings: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", nil, fmt.Errorf("claude-cli: close monitor hooks settings: %w", err)
	}
	return path, func() { _ = os.Remove(path) }, nil
}

// codexNotifyOverride renders the -c override installing captain as codex's
// notify program for one invocation. A JSON string array is valid TOML.
func codexNotifyOverride(binary string) (string, error) {
	argv := []string{binary, "hook", "monitor", "notify", "--provider", "codex"}
	value, err := json.Marshal(argv)
	if err != nil {
		return "", fmt.Errorf("encode codex notify override: %w", err)
	}
	return "notify=" + string(value), nil
}
