// Package codexhome owns the CODEX_HOME a read-only codex run starts in.
//
// Codex execpolicy `.rules` files run a matching command outside the sandbox,
// even under a read-only sandbox with approval never, so a user's
// `prefix_rule(["git","add"], decision="allow")` would let a read-only run
// write the git index. `codex exec` can skip them with --ignore-rules; the
// app-server and the interactive TUI cannot, so captain starts those in a
// captain-managed home that links only what codex needs and has no rules.
package codexhome

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/flanksource/captain/pkg/captainconfig"
)

// linked are the user-home entries a read-only run needs: the login, the
// configuration (which has no field that declares rules), and the user's
// instructions. Everything else codex writes there itself.
var linked = []string{"auth.json", "config.toml", "AGENTS.md"}

// UserHome is the home codex uses by default: $CODEX_HOME, else ~/.codex.
func UserHome() (string, error) {
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return home, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".codex"), nil
}

// ReadOnlyHome is the stable captain-managed CODEX_HOME for read-only runs. It
// lives under captain's config home rather than a cache, because codex writes
// the run's session rollouts into it and they are the run's history.
func ReadOnlyHome() (string, error) {
	home, err := captainconfig.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "codex-read-only"), nil
}

// SessionRoots are the directories codex writes rollouts to: the user's home
// and the read-only home.
func SessionRoots() ([]string, error) {
	user, err := UserHome()
	if err != nil {
		return nil, err
	}
	readOnly, err := ReadOnlyHome()
	if err != nil {
		return nil, err
	}
	return []string{filepath.Join(user, "sessions"), filepath.Join(readOnly, "sessions")}, nil
}

// PrepareReadOnly creates the read-only home and links the user's login,
// configuration and instructions into it. A rules directory in it is refused
// rather than removed: with approval never nothing should have written one.
func PrepareReadOnly() (string, error) {
	user, err := UserHome()
	if err != nil {
		return "", err
	}
	home, err := ReadOnlyHome()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", fmt.Errorf("create read-only CODEX_HOME: %w", err)
	}
	if rules := filepath.Join(home, "rules"); exists(rules) {
		return "", fmt.Errorf("read-only CODEX_HOME has an execpolicy rules directory %s; remove it, a read-only run must not load rules", rules)
	}
	if err := reconcileLogin(filepath.Join(user, "auth.json"), filepath.Join(home, "auth.json")); err != nil {
		return "", err
	}
	for _, name := range linked {
		if err := linkInto(filepath.Join(user, name), filepath.Join(home, name)); err != nil {
			return "", err
		}
	}
	return home, nil
}

// reconcileLogin keeps one login across the two homes. Codex 0.163.0 writes
// auth.json in place (FileAuthStorage::save opens it with truncate), so a
// token refresh goes through the link to the user's file. Should a codex ever
// write it by rename instead, the link becomes a regular file: a newer one is
// a refreshed login and is moved back over the user's (an atomic rename) before
// re-linking; an older one is superseded by the user's newer login. Anything
// else — no user login to compare with, or a same-age file with different
// content — is refused, so a refreshed token is never dropped.
func reconcileLogin(user, isolated string) error {
	info, err := os.Lstat(isolated)
	if errors.Is(err, fs.ErrNotExist) || err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("read-only CODEX_HOME login %s is a %s, not a link or a file", isolated, info.Mode().Type())
	}
	current, err := os.Stat(user)
	if err != nil {
		return fmt.Errorf("read-only CODEX_HOME holds its own login %s and the user login cannot be compared: %w", isolated, err)
	}
	switch {
	case info.ModTime().After(current.ModTime()):
		if err := os.Rename(isolated, user); err != nil {
			return fmt.Errorf("move the refreshed codex login %s back to %s: %w", isolated, user, err)
		}
		return nil
	case info.ModTime().Before(current.ModTime()) || sameContent(user, isolated):
		return os.Remove(isolated)
	default:
		return fmt.Errorf("read-only CODEX_HOME login %s differs from %s but is not newer; resolve it by hand", isolated, user)
	}
}

func sameContent(left, right string) bool {
	a, errA := os.ReadFile(left)
	b, errB := os.ReadFile(right)
	return errA == nil && errB == nil && string(a) == string(b)
}

// ForRun is the CODEX_HOME a codex process without --ignore-rules (the
// app-server, the interactive TUI) starts in: "" (inherit) unless the run is
// read-only, then the prepared isolated home, after refusing project rules the
// isolated home cannot hide.
func ForRun(readOnly bool, cwd string) (string, error) {
	if !readOnly {
		return "", nil
	}
	if err := RequireNoProjectAllowRules(cwd); err != nil {
		return "", err
	}
	return PrepareReadOnly()
}

// linkInto points target at source, replacing a link captain left there
// before, and removes the link when the user has no such file. A target that
// is not a link was written by something else, and is refused rather than
// deleted: codex writes config.toml and auth.json through the link.
func linkInto(source, target string) error {
	if info, err := os.Lstat(target); err == nil && info.Mode()&fs.ModeSymlink == 0 {
		return fmt.Errorf("read-only CODEX_HOME entry %s is no longer a link to %s; move its changes into %s and remove it", target, source, source)
	}
	if !exists(source) {
		if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove stale %s: %w", target, err)
		}
		return nil
	}
	if current, err := os.Readlink(target); err == nil && current == source {
		return nil
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("replace %s: %w", target, err)
	}
	if err := os.Symlink(source, target); err != nil {
		// A concurrent run may have linked it first.
		if current, readErr := os.Readlink(target); readErr == nil && current == source {
			return nil
		}
		return fmt.Errorf("link %s into the read-only CODEX_HOME: %w", source, err)
	}
	return nil
}

var (
	prefixRule      = regexp.MustCompile(`prefix_rule\s*\(`)
	nonAllowDecided = regexp.MustCompile(`decision\s*=\s*"(forbidden|prompt)"`)
)

// RequireNoProjectAllowRules refuses a working tree whose codex project layers
// carry a rule that allows a command. Codex reads `.codex/rules/*.rules` from
// every directory between the working directory and the project root (the
// nearest ancestor with .git, or the working directory alone), and the
// isolated home cannot hide them. A rule with no decision defaults to allow.
func RequireNoProjectAllowRules(cwd string) error {
	if cwd == "" {
		return nil
	}
	for _, dir := range projectLayers(cwd) {
		files, err := filepath.Glob(filepath.Join(dir, ".codex", "rules", "*.rules"))
		if err != nil {
			return err
		}
		for _, file := range files {
			data, err := os.ReadFile(file)
			if err != nil {
				return fmt.Errorf("read project execpolicy rules: %w", err)
			}
			text := string(data)
			if len(prefixRule.FindAllString(text, -1)) > len(nonAllowDecided.FindAllString(text, -1)) {
				return fmt.Errorf("project execpolicy rules %s allow commands to run outside the read-only sandbox; remove the allow rules or run without permissions.access read-only", file)
			}
		}
	}
	return nil
}

// projectLayers lists cwd and its ancestors up to the nearest one holding .git;
// with no .git anywhere, cwd alone.
func projectLayers(cwd string) []string {
	var dirs []string
	for dir := filepath.Clean(cwd); ; dir = filepath.Dir(dir) {
		dirs = append(dirs, dir)
		if exists(filepath.Join(dir, ".git")) {
			return dirs
		}
		if parent := filepath.Dir(dir); parent == dir || strings.TrimSpace(parent) == "" {
			return dirs[:1]
		}
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
