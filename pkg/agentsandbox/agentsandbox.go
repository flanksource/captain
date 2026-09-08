// Package agentsandbox registers filesystem paths as writable inside the
// sandboxes local agent CLIs run their tools in.
//
// Claude Code and codex each confine tool execution to an allowlist of writable
// directories, in different files and different formats. Anything captain writes
// from inside an agent's own sandbox — a hook's state, a plugin's record — is
// denied unless its directory appears on both lists, and the denial surfaces to
// the user as an unexplained "operation not permitted". This package is the one
// place that knows how to add a path to both.
package agentsandbox

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/flanksource/captain/pkg/claudeconfig"
	"github.com/flanksource/captain/pkg/codexconfig"
)

// Change is what one agent's configuration gained.
type Change struct {
	Agent string   `json:"agent"`
	Path  string   `json:"path"`
	Added []string `json:"added,omitempty"`
}

func (c Change) String() string {
	if len(c.Added) == 0 {
		return fmt.Sprintf("%s sandbox: already writable in %s", c.Agent, c.Path)
	}
	return fmt.Sprintf("%s sandbox: added %v to %s", c.Agent, c.Added, c.Path)
}

// EnsureWritable makes paths writable for every local agent CLI captain knows
// how to configure, and reports what each one gained.
//
// Both agents are always attempted: a machine with only one of them installed
// still gets the other configured, and a failure in one is not allowed to hide
// the other's result. Paths are made absolute first, because a relative entry in
// either allowlist is interpreted against the agent's own working directory.
func EnsureWritable(paths ...string) ([]Change, error) {
	absolute, err := absolutePaths(paths)
	if err != nil {
		return nil, err
	}
	if len(absolute) == 0 {
		return nil, nil
	}

	var changes []Change
	var failures []error

	claudePath, claudeErr := claudeconfig.Path()
	if claudeErr != nil {
		failures = append(failures, claudeErr)
	} else {
		added, err := claudeconfig.EnsureSandboxWritable(absolute)
		if err != nil {
			failures = append(failures, err)
		} else {
			changes = append(changes, Change{Agent: "claude", Path: claudePath, Added: added})
		}
	}

	codexPath, codexErr := codexconfig.Path()
	if codexErr != nil {
		failures = append(failures, codexErr)
	} else {
		added, err := codexconfig.EnsureWritableRoots(absolute)
		if err != nil {
			failures = append(failures, err)
		} else {
			changes = append(changes, Change{Agent: "codex", Path: codexPath, Added: added})
		}
	}
	return changes, errors.Join(failures...)
}

func absolutePaths(paths []string) ([]string, error) {
	absolute := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		resolved, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve sandbox path %q: %w", path, err)
		}
		absolute = append(absolute, resolved)
	}
	return absolute, nil
}
