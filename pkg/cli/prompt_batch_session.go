package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
)

type promptSessionBinding struct {
	BatchID   uuid.UUID
	SessionID uuid.UUID
}

type promptBatchRun struct {
	SessionID   uuid.UUID
	Runtime     api.Model
	FanOutIndex int
}

func (r promptBatchRun) selector() string {
	if r.FanOutIndex > 0 {
		return fmt.Sprintf("item %d", r.FanOutIndex)
	}
	return runtimeSelector(r.Runtime)
}

func (r promptBatchRun) item() PromptRunItem {
	return PromptRunItem{
		SessionID: r.SessionID.String(), Selector: r.selector(), FanOutIndex: r.FanOutIndex,
		Model: r.Runtime.Name, Provider: providerName(r.Runtime.Provider), Mode: string(r.Runtime.Mode),
		Effort: string(r.Runtime.Effort),
	}
}

type promptBatchSession struct {
	ID   uuid.UUID
	Runs []promptBatchRun
}

func createPromptBatchSessions(ctx context.Context, rendered PromptRenderResult, runtimes []api.Model) (promptBatchSession, error) {
	if err := validatePromptRuntimes(runtimes); err != nil {
		return promptBatchSession{}, err
	}
	batch := promptBatchSession{ID: uuid.New(), Runs: make([]promptBatchRun, len(runtimes))}
	children := make([]database.CreateSessionInput, len(runtimes))
	for i, runtime := range runtimes {
		batch.Runs[i] = promptBatchRun{SessionID: uuid.New(), Runtime: runtime}
		children[i] = database.CreateSessionInput{
			ID: batch.Runs[i].SessionID, Source: "captain",
			Provider: providerName(runtime.Provider), HostID: captainHostID(),
			CWD: rendered.Input.Cwd(), Title: runtime.Name, InitialPrompt: rendered.Input.Prompt.User,
			AgentType: "model", Description: runtimeSelector(runtime),
		}
	}
	root := database.CreateSessionInput{
		ID: batch.ID, Source: "captain", Provider: "multi-model", HostID: captainHostID(),
		CWD: rendered.Input.Cwd(), Title: rendered.Name, InitialPrompt: rendered.Input.Prompt.User,
		AgentType: "batch", Description: fmt.Sprintf("%d model comparison", len(runtimes)),
	}
	if err := createPromptSessionTree(ctx, root, children); err != nil {
		return promptBatchSession{}, err
	}
	return batch, nil
}

func createPromptSessionTree(ctx context.Context, root database.CreateSessionInput, children []database.CreateSessionInput) error {
	db, err := captainDefaultDB(ctx)
	if err != nil {
		return err
	}
	_, err = db.CreateSessionTree(ctx, database.CreateSessionTreeInput{
		Root:     root,
		Children: children,
	})
	return err
}

func finishPromptBatch(ctx context.Context, id uuid.UUID, succeeded, failed int) {
	reason := fmt.Sprintf("%d succeeded, %d failed", succeeded, failed)
	updatePromptSessionLifecycle(context.WithoutCancel(ctx), id, batchLifecycle(succeeded, failed), reason)
}

func validatePromptRuntimes(runtimes []api.Model) error {
	if len(runtimes) < 2 {
		return fmt.Errorf("multi-model execution requires at least two runtimes")
	}
	seen := map[string]struct{}{}
	for i := range runtimes {
		runtime := runtimes[i]
		if err := runtime.Validate(); err != nil {
			return fmt.Errorf("runtime %d: %w", i+1, err)
		}
		// A batch runs what it is handed, so every member must already be
		// resolved: Runtime() is the assertion, not a derivation step.
		if _, _, err := runtime.Runtime(); err != nil {
			return fmt.Errorf("runtime %d: %w", i+1, err)
		}
		key := runtimeSelector(runtime)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("runtime %d duplicates %s", i+1, key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func runtimeSelector(runtime api.Model) string {
	parts := []string{string(runtime.Mode), runtime.Name}
	if runtime.Effort != api.EffortNone {
		parts = append(parts, string(runtime.Effort))
	}
	return strings.Join(parts, ":")
}

func promptBinding(batch promptBatchSession, index int) *promptSessionBinding {
	return &promptSessionBinding{BatchID: batch.ID, SessionID: batch.Runs[index].SessionID}
}

func updatePromptSessionLifecycle(ctx context.Context, id uuid.UUID, lifecycle database.SessionLifecycleStatus, reason string) {
	db, err := captainDefaultDB(ctx)
	if err != nil {
		log.Errorf("open database for session %s lifecycle: %v", id, err)
		return
	}
	if _, err := db.UpdateSessionLifecycle(ctx, id, lifecycle, reason); err != nil {
		log.Errorf("update session %s lifecycle to %s: %v", id, lifecycle, err)
	}
}

func batchLifecycle(succeeded, failed int) database.SessionLifecycleStatus {
	switch {
	case failed == 0:
		return database.SessionLifecycleSucceeded
	case succeeded == 0:
		return database.SessionLifecycleFailed
	default:
		return database.SessionLifecyclePartial
	}
}
