package commit

import "github.com/flanksource/captain/pkg/api"

// HooksForWorkflow builds the run's commit hooks from a spec's Workflow — one
// per declared policy, in declaration order. Returns nil when the workflow
// commits nothing, which is the default: captain never commits unless asked to.
//
// Sibling of verify.HooksForWorkflow, and returns []any for the same reason —
// the runner's hook list is heterogeneous. Register these ahead of the worktree
// plugin so a run-phase commit is cut while the worktree is still live, and
// therefore has something for the merge to take.
func HooksForWorkflow(wf *api.Workflow) []any {
	if wf == nil {
		return nil
	}
	hooks := make([]any, 0, len(wf.Commits))
	for _, c := range wf.Commits {
		hooks = append(hooks, New(c))
	}
	if len(hooks) == 0 {
		return nil
	}
	return hooks
}
