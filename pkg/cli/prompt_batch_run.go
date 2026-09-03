package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/clicky/task"
	flanksourceContext "github.com/flanksource/commons/context"
)

func launchAsyncBatch(ctx context.Context, id string, rendered PromptRenderResult, runtimes []api.Model, chat bool) (PromptRunResult, error) {
	if rendered.Input.SessionID != "" {
		return PromptRunResult{}, errors.New("a multi-model run cannot resume one provider session")
	}
	batch, err := createPromptBatchSessions(ctx, rendered, runtimes)
	if err != nil {
		return PromptRunResult{}, err
	}
	updatePromptSessionLifecycle(ctx, batch.ID, database.SessionLifecycleRunning, "")
	group := task.StartGroup[PromptRunSummary](
		"prompt "+rendered.Name,
		task.WithGroupID(batch.ID.String()),
		task.WithKind("prompt"),
		task.WithLabels(promptTaskLabelsWithID(rendered, id, "multi")),
		task.WithConcurrency(len(batch.Runs)),
	)
	handles := make([]task.TypedTask[PromptRunSummary], len(batch.Runs))
	result := PromptRunResult{
		BatchID: batch.ID.String(), Status: "running", Chat: chat,
		Total: len(batch.Runs), Runs: make([]PromptRunItem, len(batch.Runs)),
	}
	for i := range batch.Runs {
		i := i
		run := batch.Runs[i]
		binding := promptBinding(batch, i)
		variant := renderVariant(rendered, run.Runtime, nil)
		runID := run.SessionID.String()
		stream := promptRuns.create(runID)
		capabilities := chatCapabilitiesFor(variant.Provider, variant.Mode)
		stream.setRun(PromptRunFrame{
			RunID: runID, SessionID: runID, Status: "running", Chat: chat,
			Model: variant.Model, Provider: variant.Provider, Mode: variant.Mode, Capabilities: capabilities,
		})
		updatePromptSessionLifecycle(ctx, run.SessionID, database.SessionLifecycleRunning, "")
		result.Runs[i] = PromptRunItem{
			RunID: runID, SessionID: runID, Selector: runtimeSelector(run.Runtime),
			Status: "running", Model: run.Runtime.Name, Provider: providerName(run.Runtime.Provider), Mode: string(run.Runtime.Mode),
			Effort: string(run.Runtime.Effort), Chat: chat, Capabilities: capabilities,
		}
		handles[i] = group.Add(runtimeSelector(run.Runtime), func(_ flanksourceContext.Context, t *task.Task) (PromptRunSummary, error) {
			timeout, err := renderedTimeout(variant)
			if err != nil {
				return PromptRunSummary{}, err
			}
			if chat {
				chatSession := newChatSession(runID, variant, timeout, stream, binding)
				promptChats.register(chatSession)
				return chatSession.run(t)
			}
			summary, runErr := runPromptStream(t, variant, timeout, runID, stream, binding)
			if runErr != nil {
				persistPromptRun(context.WithoutCancel(t.Context()), promptRunRecordInput{
					Rendered: variant, RunID: runID, Binding: binding,
					Model: run.Runtime.Name, Provider: run.Runtime.Provider, Mode: run.Runtime.Mode, Error: runErr.Error(),
				})
			}
			return summary, runErr
		}, task.WithModel(run.Runtime.Name), task.WithPrompt(rendered.Input.Prompt.User))
	}

	task.StartTask("finalize prompt batch", func(_ flanksourceContext.Context, t *task.Task) (PromptRunSummary, error) {
		succeeded, failed := 0, 0
		for i := range handles {
			summary, runErr := handles[i].GetResult()
			if runErr != nil || !summary.Success {
				failed++
			} else {
				succeeded++
			}
		}
		reason := fmt.Sprintf("%d succeeded, %d failed", succeeded, failed)
		updatePromptSessionLifecycle(context.WithoutCancel(t.Context()), batch.ID, batchLifecycle(succeeded, failed), reason)
		t.Success()
		return PromptRunSummary{RunID: batch.ID.String(), Success: failed == 0}, nil
	}, task.WithIdentity("prompt-batch-finalize-"+batch.ID.String()))
	return result, nil
}
