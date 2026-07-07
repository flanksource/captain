package worktree

import "fmt"

// WorktreeMerge controls whether PostRun merges the run's branch back into
// Trunk via `wt merge`. These reference the run/verify outcome (Failed/
// Verified), which commons-db's shell.Worktree does not model, so they live in
// this captain-layer package rather than pkg/api.
type WorktreeMerge string

const (
	// WorktreeMergeAlways merges unconditionally.
	WorktreeMergeAlways WorktreeMerge = "always"
	// WorktreeMergeNever never merges (the default — matches Plugin{}'s
	// zero-value behaviour of leaving the branch for the caller to handle).
	WorktreeMergeNever WorktreeMerge = "never"
	// WorktreeMergeOnSuccess merges only when the run completed without error.
	WorktreeMergeOnSuccess WorktreeMerge = "onSuccess"
	// WorktreeMergeOnVerify merges only when every Verify hook passed.
	WorktreeMergeOnVerify WorktreeMerge = "onVerify"
)

// AllWorktreeMerges lists the merge policies in canonical order.
func AllWorktreeMerges() []WorktreeMerge {
	return []WorktreeMerge{WorktreeMergeAlways, WorktreeMergeNever, WorktreeMergeOnSuccess, WorktreeMergeOnVerify}
}

// Valid reports whether m is a recognised merge policy (including the ""
// default, treated as WorktreeMergeNever).
func (m WorktreeMerge) Valid() bool {
	switch m {
	case "", WorktreeMergeAlways, WorktreeMergeNever, WorktreeMergeOnSuccess, WorktreeMergeOnVerify:
		return true
	default:
		return false
	}
}

// Validate fails loud on an unknown merge policy, naming the valid set.
func (m WorktreeMerge) Validate() error {
	if m.Valid() {
		return nil
	}
	return fmt.Errorf("invalid worktree merge policy %q; want one of: always, never, onSuccess, onVerify", m)
}

// shouldMerge decides whether PostRun should merge, given the run's outcome.
func (m WorktreeMerge) shouldMerge(failed, verified bool) bool {
	switch m {
	case WorktreeMergeAlways:
		return true
	case WorktreeMergeOnSuccess:
		return !failed
	case WorktreeMergeOnVerify:
		return verified
	default: // "", WorktreeMergeNever
		return false
	}
}

// WorktreeCleanup controls whether PostRun removes the worktree via `wt
// remove`.
type WorktreeCleanup string

const (
	// WorktreeCleanupAlways removes unconditionally (the default — matches
	// Plugin{}'s zero-value behaviour of always tearing down).
	WorktreeCleanupAlways WorktreeCleanup = "always"
	// WorktreeCleanupNever keeps the worktree for inspection.
	WorktreeCleanupNever WorktreeCleanup = "never"
	// WorktreeCleanupOnMerge removes only after a merge happened.
	WorktreeCleanupOnMerge WorktreeCleanup = "onMerge"
	// WorktreeCleanupOnVerify removes only when every Verify hook passed.
	WorktreeCleanupOnVerify WorktreeCleanup = "onVerify"
)

// AllWorktreeCleanups lists the cleanup policies in canonical order.
func AllWorktreeCleanups() []WorktreeCleanup {
	return []WorktreeCleanup{WorktreeCleanupAlways, WorktreeCleanupNever, WorktreeCleanupOnMerge, WorktreeCleanupOnVerify}
}

// Valid reports whether c is a recognised cleanup policy (including the ""
// default, treated as WorktreeCleanupAlways).
func (c WorktreeCleanup) Valid() bool {
	switch c {
	case "", WorktreeCleanupAlways, WorktreeCleanupNever, WorktreeCleanupOnMerge, WorktreeCleanupOnVerify:
		return true
	default:
		return false
	}
}

// Validate fails loud on an unknown cleanup policy, naming the valid set.
func (c WorktreeCleanup) Validate() error {
	if c.Valid() {
		return nil
	}
	return fmt.Errorf("invalid worktree cleanup policy %q; want one of: always, never, onMerge, onVerify", c)
}

// shouldCleanup decides whether PostRun should remove the worktree, given
// whether a merge happened and whether every Verify hook passed.
func (c WorktreeCleanup) shouldCleanup(merged, verified bool) bool {
	switch c {
	case WorktreeCleanupNever:
		return false
	case WorktreeCleanupOnMerge:
		return merged
	case WorktreeCleanupOnVerify:
		return verified
	default: // "", WorktreeCleanupAlways
		return true
	}
}
