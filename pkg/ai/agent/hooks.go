package agent

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

// WorkspaceIsolator is implemented by a hook that relocates the run into its own
// working tree — a git worktree from Spec.Setup.Checkout, or the `wt` worktree
// plugin. Two of them in one run create two trees and use one, so each declares
// itself here and calls EnsureSingleIsolator before acting.
type WorkspaceIsolator interface {
	Name() string
	// IsolatesWorkspace reports whether this hook will relocate the run given
	// hc's request; a hook whose isolation is configured on the spec answers
	// from hc.Request rather than from its own fields.
	IsolatesWorkspace(*HookContext) bool
}

// EnsureSingleIsolator fails when more than one registered hook would relocate
// the run into its own working tree. Silently creating two and working in one is
// the failure this exists to prevent: the run edits a tree nobody merges.
func (hc *HookContext) EnsureSingleIsolator() error {
	var names []string
	for _, h := range hc.Hooks {
		if iso, ok := h.(WorkspaceIsolator); ok && iso.IsolatesWorkspace(hc) {
			names = append(names, iso.Name())
		}
	}
	if len(names) > 1 {
		return fmt.Errorf("agent: hooks %s each isolate the run in their own working tree; register exactly one", strings.Join(names, " and "))
	}
	return nil
}

// VerifyResult is a Verify hook's judgement on an iteration. When !Valid, Retry
// (if non-nil) is the exact next request to run — the hook bakes its feedback
// into that request's prompt. Report is the typed verdict every verifier
// produces (what captain persists per iteration and the webapp renders);
// Iteration is the loop iteration it judged.
type VerifyResult struct {
	Valid     bool
	Retry     *ai.Request
	Report    *api.VerifyReport
	Iteration int
}

// PreRun runs once before the loop.
type PreRun interface {
	Name() string
	PreRun(*HookContext) error
}

// Verify runs after each completed iteration and votes.
type Verify interface {
	Name() string
	Verify(*HookContext) (VerifyResult, error)
}

// Post runs after a lifecycle phase completes (commit, teardown, checkpointing).
// A hook declares which phases it handles via Phases(); the runner dispatches
// only those, and always — including on the failure path, so a hook can make
// work durable or tear down after an error.
type Post interface {
	Name() string
	Phases() []Phase
	Post(*HookContext, Phase) error
}

// Output produces the workflow's typed final result. Optional; when absent the
// runner leaves Result.Output at its zero value.
type Output[T any] interface {
	Name() string
	Output(*HookContext) (T, error)
}
