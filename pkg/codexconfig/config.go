// Package codexconfig manages the notify entry in codex's user configuration
// at ~/.codex/config.toml. Codex supports exactly one notify program, so the
// package only ever installs or updates a captain-owned entry and refuses to
// clobber a foreign notifier. Edits are line-level to preserve the user's
// comments and formatting; go-toml is used for validation and detection only.
package codexconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// pathOverride lets tests redirect Path() to a temp directory without touching
// $HOME. Empty string means "use os.UserHomeDir".
var pathOverride string

// SetPathForTesting redirects Path() to the given absolute file path. Tests
// must call it with t.Cleanup(func() { SetPathForTesting("") }).
func SetPathForTesting(p string) { pathOverride = p }

// Path returns the absolute path to codex's user config file.
func Path() (string, error) {
	if pathOverride != "" {
		return pathOverride, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".codex", "config.toml"), nil
}

// IsCaptainNotify reports whether a notify argv is captain's hook receiver,
// regardless of where the captain binary lives.
func IsCaptainNotify(argv []string) bool {
	return len(argv) >= 4 && argv[1] == "hook" && argv[2] == "monitor" && argv[3] == "notify"
}

// SetNotify installs argv as codex's notify program, preserving the rest of
// the file byte-for-byte. It fails loudly on unparseable TOML, on a notify
// value that does not sit on a single line, and on a foreign (non-captain)
// notify entry — codex has one notify slot and the user's notifier wins.
func SetNotify(argv []string) (string, error) {
	if !IsCaptainNotify(argv) {
		return "", fmt.Errorf("refusing to install non-captain notify argv %v", argv)
	}
	path, err := Path()
	if err != nil {
		return "", err
	}
	line, err := notifyLine(argv)
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", fmt.Errorf("ensure %s: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
			return "", fmt.Errorf("write %s: %w", path, err)
		}
		return fmt.Sprintf("codex notify: installed in %s (created)", path), nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}

	var config struct {
		Notify []string `toml:"notify"`
	}
	if err := toml.Unmarshal(data, &config); err != nil {
		return "", fmt.Errorf("parse %s: %w (fix the file before installing the captain notify hook)", path, err)
	}

	if config.Notify == nil {
		updated := insertTopLevelLine(string(data), line)
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			return "", fmt.Errorf("write %s: %w", path, err)
		}
		return fmt.Sprintf("codex notify: installed in %s", path), nil
	}
	if !IsCaptainNotify(config.Notify) {
		return "", fmt.Errorf("%s already sets notify = %v; codex supports one notify program — remove it first to let captain monitor codex sessions", path, config.Notify)
	}

	updated, err := replaceNotifyLine(string(data), line)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if updated == string(data) {
		return fmt.Sprintf("codex notify: already installed in %s", path), nil
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return fmt.Sprintf("codex notify: updated in %s", path), nil
}

// notifyLine renders `notify = ["…", …]`. A JSON string array is valid TOML.
func notifyLine(argv []string) (string, error) {
	value, err := json.Marshal(argv)
	if err != nil {
		return "", fmt.Errorf("encode notify argv: %w", err)
	}
	return "notify = " + string(value), nil
}

// insertTopLevelLine adds a top-level assignment: TOML requires top-level keys
// to precede the first table header, so the line goes right above it (or at
// the end of a table-free file).
func insertTopLevelLine(content, line string) string {
	lines := strings.Split(content, "\n")
	for i, existing := range lines {
		if strings.HasPrefix(strings.TrimSpace(existing), "[") {
			lines = append(lines[:i], append([]string{line, ""}, lines[i:]...)...)
			return strings.Join(lines, "\n")
		}
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + line + "\n"
}

// replaceNotifyLine swaps the existing top-level notify assignment for the new
// line. The assignment must sit on a single line: TOML allows multiline
// arrays, and rewriting one line-by-line would corrupt the file.
func replaceNotifyLine(content, line string) (string, error) {
	lines := strings.Split(content, "\n")
	for i, existing := range lines {
		trimmed := strings.TrimSpace(existing)
		if strings.HasPrefix(trimmed, "[") {
			break // notify was parsed at top level but its assignment wasn't found above the first table
		}
		if !strings.HasPrefix(trimmed, "notify") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "notify"))
		if !strings.HasPrefix(rest, "=") {
			continue
		}
		var single struct {
			Notify []string `toml:"notify"`
		}
		if err := toml.Unmarshal([]byte(trimmed), &single); err != nil || single.Notify == nil {
			return "", fmt.Errorf("notify assignment spans multiple lines; update it manually")
		}
		lines[i] = line
		return strings.Join(lines, "\n"), nil
	}
	return "", fmt.Errorf("notify assignment not found on a single top-level line; update it manually")
}
