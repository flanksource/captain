package promptrun

import (
	"context"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent/commit"
	"github.com/flanksource/captain/pkg/ai/agent/setup"
	"github.com/flanksource/captain/pkg/ai/agent/verify"
)

// Hooks assembles the run's hook list in the order the runner dispatches them:
//
//	commit → cmd → prompt → fixture → caller → setup
//
// The order is the run's safety contract, because agent.Runner dispatches Post
// hooks in list order at every phase. Commit hooks lead so a PhaseRun squash is
// cut before any teardown can take the tree it commits from. The workflow's
// checks run cheap to expensive (verify.HooksFor's own order), so a run that is
// going to fail fails on the fast check. The caller's hooks sit between the
// checks and setup: a host commit pipeline at PhaseRun still sees a live
// worktree, and a host PreRun runs before the tree is relocated. Setup trails so
// that its teardown is the last thing to happen.
//
// It is exported so a host can assert the list it will run — and so a caller
// that drives verifiers out of loop can still ask what a spec declares.
func Hooks(ctx context.Context, in Input, provider ai.Provider) ([]any, error) {
	opts := in.Verify
	if opts.Provider == nil {
		opts.Provider = provider
	}
	verifyHooks, err := verify.HooksFor(ctx, in.Request.Workflow, opts)
	if err != nil {
		return nil, err
	}
	hooks := append(commit.HooksForWorkflow(in.Request.Workflow), verifyHooks...)
	hooks = append(hooks, in.Hooks...)
	if in.Provider == nil && in.Request.Setup != nil {
		hooks = append(hooks, &setup.Plugin{})
	}
	return hooks, nil
}
