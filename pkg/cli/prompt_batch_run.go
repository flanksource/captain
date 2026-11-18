package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/clicky/task"
	flanksourceContext "github.com/flanksource/commons/context"
)

func launchAsyncBatch(ctx context.Context, launch promptLaunch) (PromptRunResult, error) {
	rendered := launch.Rendered
	if err := rendered.validateVariants(); err != nil {
		return PromptRunResult{}, err
	}
	if rendered.Input.SessionID != "" {
		return PromptRunResult{}, errors.New("a multi-model run cannot resume one provider session")
	}
	children := make([]PromptRenderResult, len(rendered.variants))
	for i, variant := range rendered.variants {
		children[i] = renderVariant(rendered, variant)
	}
	timeouts, err := promptBatchTimeouts(children)
	if err != nil {
		return PromptRunResult{}, err
	}
	batch, err := createPromptBatchSessions(ctx, rendered, rendered.Runtimes)
	if err != nil {
		return PromptRunResult{}, err
	}
	return launchPromptBatch(ctx, launch, asyncPromptBatch{
		Session: batch, Children: children, Timeouts: timeouts, Mode: "multi", Workers: len(children),
	}), nil
}

func promptBatchTimeouts(children []PromptRenderResult) ([]time.Duration, error) {
	timeouts := make([]time.Duration, len(children))
	for i, child := range children {
		timeout, err := renderedTimeout(child)
		if err != nil {
			return nil, fmt.Errorf("run %d: %w", i+1, err)
		}
		timeouts[i] = timeout
	}
	return timeouts, nil
}

type asyncPromptBatch struct {
	Session  promptBatchSession
	Children []PromptRenderResult
	Timeouts []time.Duration
	Mode     string
	Workers  int
}

func launchPromptBatch(ctx context.Context, launch promptLaunch, plan asyncPromptBatch) PromptRunResult {
	batch := plan.Session
	chat := launch.Chat
	updatePromptSessionLifecycle(ctx, batch.ID, database.SessionLifecycleRunning, "")
	var group *task.Group
	handles := make([]task.TypedTask[PromptRunSummary], len(batch.Runs))
	result := PromptRunResult{
		BatchID: batch.ID.String(), Status: "running", Chat: chat,
		Total: len(batch.Runs), Runs: make([]PromptRunItem, len(batch.Runs)),
	}
	for i := range batch.Runs {
		i := i
		run := batch.Runs[i]
		binding := promptBinding(batch, i)
		variant := plan.Children[i]
		runID := run.SessionID.String()
		stream := promptRuns.create(runID)
		stream.observer = launch.OnEvent
		capabilities := chatCapabilitiesFor(variant.Provider, variant.Mode)
		stream.setRun(PromptRunFrame{
			RunID: runID, SessionID: runID, Status: "running", Chat: chat,
			Model: variant.Model, Provider: variant.Provider, Mode: variant.Mode, Capabilities: capabilities,
		})
		item := run.item()
		item.RunID, item.Status, item.Chat, item.Capabilities = runID, "running", chat, capabilities
		if variant.Skip != nil {
			item.Status, item.SkipReason = "skipped", variant.Skip.Reason
			result.Runs[i] = item
			result.Skipped++
			updatePromptSessionLifecycle(ctx, run.SessionID, database.SessionLifecycleSucceeded, item.SkipReason)
			stream.complete(PromptRunSummary{RunID: runID, SessionID: runID, Success: true})
			continue
		}
		if group == nil {
			group = launch.runGroup(batch.ID.String(), plan.Mode, task.WithConcurrency(plan.Workers))
		}
		updatePromptSessionLifecycle(ctx, run.SessionID, database.SessionLifecycleRunning, "")
		result.Runs[i] = item
		timeout := plan.Timeouts[i]
		options := []task.Option{task.WithGroup(group), task.WithModel(run.Runtime.Name), task.WithPrompt(variant.Input.Prompt.User)}
		if chat {
			chatSession := newChatSession(runID, variant, timeout, stream, binding)
			promptChats.register(chatSession)
			handles[i] = chatSession.start(launch.taskName(run.selector()), options...)
			continue
		}
		handles[i] = task.StartTask(launch.taskName(run.selector()), func(_ flanksourceContext.Context, t *task.Task) (PromptRunSummary, error) {
			return runPromptStream(t, variant, timeout, runID, stream, binding)
		}, options...)
	}

	task.StartTask("finalize prompt batch", func(_ flanksourceContext.Context, t *task.Task) (PromptRunSummary, error) {
		succeeded, failed := 0, 0
		var failures error
		for i := range handles {
			if plan.Children[i].Skip != nil {
				succeeded++
				continue
			}
			summary, runErr := handles[i].GetResult()
			if runErr != nil || !summary.Success {
				failed++
				if runErr == nil {
					runErr = fmt.Errorf("run %s did not succeed: %s", summary.RunID, summary.Error)
				}
				failures = errors.Join(failures, runErr)
			} else {
				succeeded++
			}
		}
		finishPromptBatch(t.Context(), batch.ID, succeeded, failed)
		summary := PromptRunSummary{RunID: batch.ID.String(), SessionID: batch.ID.String(), Success: failed == 0}
		if failures != nil {
			summary.Error = failures.Error()
		}
		if launch.OnComplete != nil {
			launch.OnComplete(summary)
		}
		t.Success()
		return summary, nil
	}, task.WithIdentity("prompt-batch-finalize-"+batch.ID.String()), task.WithSlots(0))
	return result
}
