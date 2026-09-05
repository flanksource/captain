package promptrun

import (
	"context"
	"fmt"

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
//
// Input.CallerOwnsCommits drops the leading commit hooks: the host commits, and
// its own hooks keep their position between the checks and setup.
func Hooks(ctx context.Context, in Input, provider ai.Provider) ([]any, error) {
	if err := validateCommitOwnership(in); err != nil {
		return nil, err
	}
	opts := in.Verify
	if opts.Provider == nil {
		opts.Provider = provider
	}
	if opts.RunSpec == nil {
		// A verifier that runs an agent of its own inherits the run's model,
		// permissions and budget from here. It is the resolved request — the same
		// one the runner executes — and read-only to a factory.
		opts.RunSpec = &in.Request
	}
	verifyHooks, err := verify.HooksFor(ctx, in.Request.Workflow, opts)
	if err != nil {
		return nil, err
	}
	var hooks []any
	if !in.CallerOwnsCommits {
		hooks = append(hooks, commit.HooksForWorkflow(in.Request.Workflow)...)
	}
	hooks = append(hooks, verifyHooks...)
	hooks = append(hooks, in.Hooks...)
	if in.Provider == nil && in.Request.Setup != nil {
		hooks = append(hooks, &setup.Plugin{})
	}
	return hooks, nil
}

func validateCommitOwnership(in Input) error {
	if in.CallerOwnsCommits && len(in.Hooks) == 0 {
		return fmt.Errorf("promptrun: CallerOwnsCommits is set but Input.Hooks is empty: nothing would commit")
	}
	return nil
}
