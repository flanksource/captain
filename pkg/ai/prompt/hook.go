package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// HookMode is how a hook prompt's output is folded into the content it hooks.
type HookMode string

const (
	HookReplace HookMode = "replace"
	HookPrepend HookMode = "prepend"
	HookAppend  HookMode = "append"
)

// HookSpec is the `hook:` frontmatter block that turns a .prompt file into a
// lifecycle hook. The owner of the event (e.g. pkg/plans for plan.revision)
// discovers the files and decides which events it supports.
type HookSpec struct {
	// On is the event the hook runs on, e.g. "plan.revision".
	On string `yaml:"on"`
	// Mode is replace, prepend or append.
	Mode HookMode `yaml:"mode"`
	// Field selects one string field of the structured output; empty uses the
	// response text.
	Field string `yaml:"field,omitempty"`
	// Disabled turns off a same-named hook from an earlier prompt source.
	Disabled bool `yaml:"disabled,omitempty"`
}

// Hook returns the template's `hook:` frontmatter, or nil when it declares none.
func (t *Template) Hook() (*HookSpec, error) {
	loc := frontmatterRe.FindStringSubmatch(t.source)
	if loc == nil {
		return nil, nil
	}
	var fm struct {
		Hook *HookSpec `yaml:"hook"`
	}
	if err := yaml.Unmarshal([]byte(loc[1]), &fm); err != nil {
		return nil, fmt.Errorf("parse prompt %s frontmatter: %w", t.name, err)
	}
	if fm.Hook == nil || fm.Hook.Disabled {
		return fm.Hook, nil
	}
	if strings.TrimSpace(fm.Hook.On) == "" {
		return nil, fmt.Errorf("prompt %s hook: `on` is required", t.name)
	}
	switch fm.Hook.Mode {
	case HookReplace, HookPrepend, HookAppend:
	default:
		return nil, fmt.Errorf("prompt %s hook: mode %q must be one of replace, prepend, append", t.name, fm.Hook.Mode)
	}
	return fm.Hook, nil
}

// Name is the name render errors are reported against (a path for files).
func (t *Template) Name() string { return t.name }

// ResolveDir resolves a configured prompt directory: `~` expands to the home
// directory, relative paths resolve against base, and the result must be an
// existing directory.
func ResolveDir(raw, base string) (string, error) {
	dir := strings.TrimSpace(raw)
	if dir == "" {
		return "", fmt.Errorf("prompt dir cannot be empty")
	}
	if strings.HasPrefix(dir, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		switch {
		case dir == "~":
			dir = home
		case strings.HasPrefix(dir, "~/"):
			dir = filepath.Join(home, dir[2:])
		default:
			return "", fmt.Errorf("unsupported home-relative prompt dir %q", raw)
		}
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(base, dir)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("prompt dir %s: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("prompt dir %s is not a directory", abs)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return filepath.Clean(abs), nil
}
