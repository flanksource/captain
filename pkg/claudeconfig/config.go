// Package claudeconfig manages Claude Code's user settings at
// ~/.claude/settings.json. It only ever adds to captain-relevant keys and never
// rewrites unrelated settings, mirroring how pkg/codexconfig treats codex's
// config.toml.
package claudeconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/flanksource/captain/pkg/claude"
)

// pathOverride lets tests redirect Path() to a temp directory without touching
// $HOME. Empty string means "use the real Claude home".
var pathOverride string

// SetPathForTesting redirects Path() to the given absolute file path. Tests
// must call it with t.Cleanup(func() { SetPathForTesting("") }).
func SetPathForTesting(p string) { pathOverride = p }

// Path returns the absolute path to Claude Code's user settings file.
func Path() (string, error) {
	if pathOverride != "" {
		return pathOverride, nil
	}
	home := claude.GetClaudeHome()
	if home == "" {
		return "", errors.New("resolve home directory")
	}
	return filepath.Join(home, "settings.json"), nil
}

// EnsureSandboxWritable adds paths to sandbox.filesystem.allowWrite, the
// allowlist Claude Code's tool sandbox checks before permitting a write. It
// returns the paths it had to add; an empty slice means every path was already
// allowed.
//
// Claude Code denies writes outside this list silently from the tool's point of
// view — the command simply fails with "operation not permitted" — so anything
// captain writes from inside an agent's own sandbox has to be registered here
// first.
func EnsureSandboxWritable(paths []string) ([]string, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	settings, err := readSettings(path)
	if err != nil {
		return nil, err
	}

	sandbox := childObject(settings, "sandbox")
	filesystem := childObject(sandbox, "filesystem")
	existing, ok := filesystem["allowWrite"].([]any)
	if !ok && filesystem["allowWrite"] != nil {
		return nil, fmt.Errorf("%s: sandbox.filesystem.allowWrite is not a list", path)
	}

	allowed := make(map[string]bool, len(existing))
	for _, entry := range existing {
		if value, ok := entry.(string); ok {
			allowed[value] = true
		}
	}
	var added []string
	for _, candidate := range paths {
		if allowed[candidate] {
			continue
		}
		allowed[candidate] = true
		existing = append(existing, candidate)
		added = append(added, candidate)
	}
	if len(added) == 0 {
		return nil, nil
	}

	filesystem["allowWrite"] = existing
	if err := writeSettings(path, settings); err != nil {
		return nil, err
	}
	return added, nil
}

// readSettings loads the settings document, treating a missing file as an empty
// one — sandbox configuration must be installable on a fresh machine. A file
// that exists but does not parse is an error: silently replacing it would
// discard the user's settings.
func readSettings(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	settings := map[string]any{}
	if len(data) == 0 {
		return settings, nil
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parse %s: %w (fix the file before configuring the captain sandbox paths)", path, err)
	}
	return settings, nil
}

// writeSettings rewrites the document in place. os.WriteFile keeps an existing
// file's permissions, which matters because settings.json is often 0600.
func writeSettings(path string, settings map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("ensure %s: %w", filepath.Dir(path), err)
	}
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// childObject returns the named nested object, creating it when absent so a
// partially-configured settings file still accepts the new key.
func childObject(parent map[string]any, key string) map[string]any {
	if child, ok := parent[key].(map[string]any); ok {
		return child
	}
	child := map[string]any{}
	parent[key] = child
	return child
}
