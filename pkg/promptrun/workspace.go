package promptrun

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flanksource/captain/pkg/ai/agent/setup"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
)

// ReconcileWorkspace records the workspace of an active run started before setup
// recorded it. It changes no execution or outcome state.
func ReconcileWorkspace(ctx context.Context, db *database.DB, run *database.PromptRun) error {
	if run.Workspace != nil || (run.State != database.PromptRunStateRunning && run.State != database.PromptRunStateWaiting) {
		return nil
	}
	raw, err := json.Marshal(run.RenderedSpec)
	if err != nil {
		return fmt.Errorf("encode run %s spec: %w", run.ID, err)
	}
	var spec api.Spec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return fmt.Errorf("decode run %s spec: %w", run.ID, err)
	}
	if spec.Cwd() == "" {
		return nil
	}
	workspace, err := setup.InspectWorkspace(spec.Cwd())
	if err != nil {
		return fmt.Errorf("reconcile run %s workspace: %w", run.ID, err)
	}
	if workspace.Worktree != nil {
		workspace.Worktree.Head = workspace.Worktree.Setup
		workspace.Worktree.Base, workspace.Worktree.Setup = "", ""
	}
	updated, err := updateRun(ctx, db, run.ID, func(current *database.PromptRun) (database.UpdatePromptRunInput, bool) {
		return database.UpdatePromptRunInput{Workspace: workspace}, current.Workspace == nil && !finishedRun(current.State)
	})
	if err != nil {
		return fmt.Errorf("record run %s workspace: %w", run.ID, err)
	}
	run.Workspace, run.Version = updated.Workspace, updated.Version
	return nil
}
