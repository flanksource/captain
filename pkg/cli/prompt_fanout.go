package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/clicky/task"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/shell"
	flanksourceContext "github.com/flanksource/commons/context"
	"github.com/google/uuid"
)

type renderPromptData func(context.Context, map[string]any, bool) (PromptRenderResult, error)

type promptFanOutSource struct {
	variables map[string]any
	render    renderPromptData
	tools     []api.ToolDefinition
}

func renderWithFanOut(ctx context.Context, content string, vars map[string]any, render renderPromptData) (PromptRenderResult, error) {
	document, err := promptlib.Parse(content)
	if err != nil {
		return PromptRenderResult{}, err
	}
	rendered, err := render(ctx, vars, document.FanOut != nil)
	if err == nil && document.FanOut != nil {
		rendered.FanOut = document.FanOut
		rendered.fanOutSource = &promptFanOutSource{variables: maps.Clone(vars), render: render}
	}
	return rendered, err
}

func expandPromptFanOut(ctx context.Context, rendered PromptRenderResult) ([]PromptRenderResult, error) {
	source := rendered.fanOutSource
	if source == nil {
		return nil, errors.New("fanout requires an authored prompt")
	}
	if rendered.Input.Batch != nil || len(rendered.Runtimes) > 0 || rendered.Input.SessionID != "" || rendered.Config.SessionID != "" {
		return nil, errors.New("fanout cannot be combined with file batching, multiple runtimes, or session resumption")
	}
	for _, name := range []string{"item", "itemJSON"} {
		if _, ok := source.variables[name]; ok {
			return nil, fmt.Errorf("fanout supplies the %q variable; remove the caller value before running", name)
		}
	}
	items, err := readPromptFanOut(ctx, rendered)
	if err != nil {
		return nil, err
	}
	children := make([]PromptRenderResult, len(items))
	for i, item := range items {
		child, err := renderPromptFanOutItem(ctx, source, item)
		if err != nil {
			return nil, fmt.Errorf("fanout item %d: %w", i+1, err)
		}
		children[i] = child
	}
	return children, nil
}

func readPromptFanOut(ctx context.Context, rendered PromptRenderResult) ([]json.RawMessage, error) {
	input, err := json.Marshal(rendered.fanOutSource.variables)
	if err != nil {
		return nil, fmt.Errorf("encode fanout.exec input: %w", err)
	}
	timeout, err := renderedTimeout(rendered)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	shellCtx := dbcontext.NewContext(ctx)
	cmd, err := shell.CreateCommandFromScript(shellCtx, rendered.FanOut.Exec)
	if err != nil {
		return nil, fmt.Errorf("prepare fanout.exec: %w", err)
	}
	cmd.Stdin = bytes.NewReader(input)
	result, err := shell.RunCmd(shellCtx, shell.Exec{Chroot: rendered.Input.Cwd()}, cmd)
	if err != nil {
		if result != nil && result.Stderr != "" {
			return nil, fmt.Errorf("fanout.exec: %w: %s", err, result.Stderr)
		}
		return nil, fmt.Errorf("fanout.exec: %w", err)
	}
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(result.Stdout), &items); err != nil {
		return nil, fmt.Errorf("fanout.exec must print one JSON array: %w", err)
	}
	if items == nil {
		return nil, fmt.Errorf("fanout.exec must print one JSON array, got null")
	}
	return items, nil
}

func renderPromptFanOutItem(ctx context.Context, source *promptFanOutSource, raw json.RawMessage) (PromptRenderResult, error) {
	var item any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&item); err != nil {
		return PromptRenderResult{}, err
	}
	vars := maps.Clone(source.variables)
	vars["item"], vars["itemJSON"] = item, string(raw)
	child, err := source.render(ctx, vars, false)
	if err != nil {
		return PromptRenderResult{}, err
	}
	if child.Skip == nil && child.ValidationError != "" {
		return PromptRenderResult{}, errors.New(child.ValidationError)
	}
	if child.Input.SessionID != "" || child.Config.SessionID != "" {
		return PromptRenderResult{}, errors.New("fanout items must start fresh sessions")
	}
	return child.withCallerTools(source.tools)
}

func createFanOutSessions(ctx context.Context, rendered PromptRenderResult, children []PromptRenderResult) (promptBatchSession, error) {
	batch := promptBatchSession{ID: uuid.New(), Runs: make([]promptBatchRun, len(children))}
	sessions := make([]database.CreateSessionInput, len(children))
	for i, child := range children {
		batch.Runs[i] = promptBatchRun{SessionID: uuid.New(), Runtime: child.Input.Model, FanOutIndex: i + 1}
		session := captainPromptSession(batch.Runs[i].SessionID, child)
		session.Title = fmt.Sprintf("%s · item %d", child.Name, i+1)
		sessions[i] = session
	}
	root := captainPromptSession(batch.ID, rendered)
	root.AgentType, root.Provider = "batch", "fan-out"
	root.Description = fmt.Sprintf("%d command items", len(children))
	if err := createPromptSessionTree(ctx, root, sessions); err != nil {
		return promptBatchSession{}, err
	}
	return batch, nil
}

func launchPromptFanOut(ctx context.Context, launch promptLaunch) (PromptRunResult, error) {
	if launch.Chat || launch.SessionKey != "" {
		return PromptRunResult{}, errors.New("fanout cannot be launched as a chat or a shared session")
	}
	children, err := expandPromptFanOut(ctx, launch.Rendered)
	if err != nil {
		return PromptRunResult{}, err
	}
	if len(children) == 0 {
		if launch.OnComplete != nil {
			launch.OnComplete(PromptRunSummary{Success: true})
		}
		return PromptRunResult{Status: "completed"}, nil
	}
	timeouts, err := promptBatchTimeouts(children)
	if err != nil {
		return PromptRunResult{}, err
	}
	batch, err := createFanOutSessions(ctx, launch.Rendered, children)
	if err != nil {
		return PromptRunResult{}, err
	}
	return launchPromptBatch(ctx, launch, asyncPromptBatch{
		Session: batch, Children: children, Timeouts: timeouts,
		Mode: "fan-out", Workers: max(1, launch.Rendered.FanOut.Concurrency),
	}), nil
}

func runPromptFanOut(ctx context.Context, rendered PromptRenderResult, opts AIPromptOptions) (PromptRunResult, error) {
	start := time.Now()
	children, err := expandPromptFanOut(ctx, rendered)
	if err != nil {
		return PromptRunResult{}, err
	}
	if len(children) == 0 {
		return inputBatchesResult(nil, time.Since(start)), nil
	}
	batch, err := createFanOutSessions(ctx, rendered, children)
	if err != nil {
		return PromptRunResult{}, err
	}
	updatePromptSessionLifecycle(ctx, batch.ID, database.SessionLifecycleRunning, "")
	var group task.TypedGroup[PromptRunItem]
	runs := make([]PromptRunItem, len(children))
	tasks := make([]task.TypedTask[PromptRunItem], len(children))
	for i, child := range children {
		run := batch.Runs[i]
		runs[i] = run.item()
		if child.Skip != nil {
			runs[i].Status, runs[i].SkipReason = "skipped", child.Skip.Reason
			updatePromptSessionLifecycle(ctx, run.SessionID, database.SessionLifecycleSucceeded, child.Skip.Reason)
			continue
		}
		if group.Group == nil {
			group = task.StartGroup[PromptRunItem]("prompt "+rendered.Name, task.WithGroupID(batch.ID.String()),
				task.WithKind("prompt"), task.WithLabels(promptTaskLabels(rendered, "fan-out")), task.WithConcurrency(max(1, rendered.FanOut.Concurrency)))
		}
		tasks[i] = group.Add(run.selector(), func(_ flanksourceContext.Context, t *task.Task) (PromptRunItem, error) {
			updatePromptSessionLifecycle(t.Context(), run.SessionID, database.SessionLifecycleRunning, "")
			logRuntimeWarnings(child.Resolution.Warnings)
			result, err := executeSyncRunSingleDirect(ai.ContextWithLogger(t.Context(), t), t, child, opts, promptBinding(batch, i))
			item := inputRunItem(run.item(), child, result)
			item.SessionID = run.SessionID.String()
			return item, err
		}, task.WithContext(ctx), task.WithModel(run.Runtime.Name), task.WithPrompt(child.User))
	}
	for i, typed := range tasks {
		if children[i].Skip != nil {
			continue
		}
		item, err := typed.GetResult()
		if err != nil {
			item.Status, item.Error = "failed", err.Error()
			updatePromptSessionLifecycle(context.WithoutCancel(ctx), batch.Runs[i].SessionID, database.SessionLifecycleFailed, item.Error)
		}
		item.FanOutIndex, item.Selector, item.SessionID = runs[i].FanOutIndex, runs[i].Selector, runs[i].SessionID
		runs[i] = item
	}
	result := inputBatchesResult(runs, time.Since(start))
	result.BatchID = batch.ID.String()
	finishPromptBatch(ctx, batch.ID, result.Succeeded+result.Skipped, result.Failed)
	if result.Failed > 0 {
		return result, &InputBatchesError{Result: result}
	}
	return result, nil
}
