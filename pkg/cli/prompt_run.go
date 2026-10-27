package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/promptrun"
	clickyrpc "github.com/flanksource/clicky/rpc"
	"github.com/flanksource/clicky/task"
	flanksourceContext "github.com/flanksource/commons/context"
	"github.com/google/uuid"
)

// PromptRunSummary is the terminal payload of a run: the SSE stream ends with it
// and the task's typed result carries it.
type PromptRunSummary struct {
	RunID        string  `json:"runId,omitempty"`
	SessionID    string  `json:"sessionId,omitempty"`
	Model        string  `json:"model,omitempty"`
	Provider     string  `json:"provider,omitempty"`
	Mode         string  `json:"mode,omitempty"`
	InputTokens  int     `json:"inputTokens,omitempty"`
	OutputTokens int     `json:"outputTokens,omitempty"`
	CostUSD      float64 `json:"costUSD,omitempty"`
	Duration     string  `json:"duration,omitempty"`
	Success      bool    `json:"success"`
	Error        string  `json:"error,omitempty"`
	// Text and StructuredOutput carry the final response so a synchronous CLI
	// run routed through the stream path can return it; stream consumers read
	// the same values from the frames.
	Text             string         `json:"text,omitempty"`
	StructuredOutput map[string]any `json:"structuredOutput,omitempty"`
}

// runPromptAction renders the prompt (from an id | discovered name | .prompt
// filepath | --prompt | stdin), then executes it — synchronously on the CLI
// (returns the text + cost) or asynchronously over HTTP (returns a run handle to
// stream from). This is the single prompt-run implementation; `captain ai
// prompt` is a deprecated alias.
func runPromptAction(ctx context.Context, id string, flags map[string]string) (PromptRunResult, error) {
	_, isHTTP := clickyrpc.RequestFromContext(ctx)
	if !isHTTP {
		opts, err := actionFlagsToOptions(flags)
		if err != nil {
			return PromptRunResult{}, err
		}
		return RunPrompt(ctx, PromptInvocation{Reference: id, Options: opts})
	}
	req, err := readRenderRequest(ctx, flags)
	if err != nil {
		return PromptRunResult{}, err
	}
	rendered, err := renderPrompt(ctx, id, req)
	if err != nil {
		return PromptRunResult{}, err
	}
	return launchRendered(ctx, promptLaunch{ID: id, Rendered: rendered, Chat: req.Chat})
}

// promptLaunch is one async launch: the rendered prompt under its reference
// id, whether it is a chat, and where its task goes (see PromptInvocation's
// Group and Name).
type promptLaunch struct {
	ID       string
	Rendered PromptRenderResult
	Chat     bool
	Group    *task.Group
	Name     string
	OnEvent  func(int, ai.Event)
}

// runGroup is the group the launch's tasks join: the caller's, or a
// `prompt <name>` run group of its own keyed by groupID.
func (l promptLaunch) runGroup(groupID, mode string, opts ...task.TaskGroupOption) *task.Group {
	if l.Group != nil {
		return l.Group
	}
	return task.StartGroup[PromptRunSummary]("prompt "+l.Rendered.Name, append([]task.TaskGroupOption{
		task.WithGroupID(groupID),
		task.WithKind("prompt"),
		task.WithLabels(promptTaskLabelsWithID(l.Rendered, l.ID, mode)),
	}, opts...)...).Group
}

// taskName names one of the launch's tasks: `execute`, or a variant's
// selector, in a group of its own; the caller's name (default: the prompt's),
// suffixed with the selector, in the caller's group.
func (l promptLaunch) taskName(selector string) string {
	if l.Group == nil {
		return firstNonEmpty(selector, "execute")
	}
	name := firstNonEmpty(l.Name, l.Rendered.Name)
	if selector == "" {
		return name
	}
	return name + ":" + selector
}

// launchRendered validates a rendered prompt and starts it as an async run (or
// a batch of runs, one per runtime). It is the one launch path the HTTP action
// and LaunchPrompt share.
func launchRendered(ctx context.Context, launch promptLaunch) (PromptRunResult, error) {
	rendered, chat := launch.Rendered, launch.Chat
	if rendered.ValidationError != "" {
		return PromptRunResult{}, errors.New(rendered.ValidationError)
	}
	if chat {
		if rendered.Input.Prompt.HasSchema() {
			return PromptRunResult{}, errors.New("chat mode does not support structured-output prompts")
		}
		if workflowConfigured(rendered.Input.Workflow) {
			return PromptRunResult{}, errors.New("chat mode does not support workflow-backed prompts")
		}
	}

	if len(rendered.Runtimes) > 0 {
		return launchAsyncBatch(ctx, launch)
	}
	return launchAsyncRun(launch)
}

var executePromptRequestFunc = executePromptRequest

// launchAsyncRun starts the background clicky task + SSE stream and returns the
// run handle (the serve/web-UI contract).
func launchAsyncRun(launch promptLaunch) (PromptRunResult, error) {
	rendered, chat := launch.Rendered, launch.Chat
	timeout, err := renderedTimeout(rendered)
	if err != nil {
		return PromptRunResult{}, err
	}
	runID := uuid.NewString()
	stream := promptRuns.create(runID)
	stream.observer = launch.OnEvent
	capabilities := chatCapabilitiesFor(rendered.Provider, rendered.Mode)
	stream.setRun(PromptRunFrame{
		RunID: runID, Status: "running", Chat: chat, Model: rendered.Model,
		Provider: rendered.Provider, Mode: rendered.Mode, Capabilities: capabilities,
	})

	execute := func(_ flanksourceContext.Context, t *task.Task) (PromptRunSummary, error) {
		return runPromptStream(t, rendered, timeout, runID, stream, nil)
	}
	if chat {
		chatSession := newChatSession(runID, rendered, timeout, stream, nil)
		promptChats.register(chatSession)
		execute = func(_ flanksourceContext.Context, t *task.Task) (PromptRunSummary, error) {
			return chatSession.run(t)
		}
	}
	task.StartTask(launch.taskName(""), execute,
		task.WithGroup(launch.runGroup(runID, "")),
		task.WithModel(rendered.Model), task.WithPrompt(rendered.Input.Prompt.User))
	return PromptRunResult{
		RunID: runID, Status: "running", Model: rendered.Model, Provider: rendered.Provider, Mode: rendered.Mode,
		Chat: chat, Capabilities: capabilities,
	}, nil
}

func workflowConfigured(workflow *api.Workflow) bool {
	return workflow != nil && (workflow.Verify != nil || len(workflow.Commits) > 0 || len(workflow.After) > 0 || workflow.AutoVerifyWithoutFixture)
}

// requiresRunner reports a spec the direct provider call cannot honour: its
// workflow's hooks and its inline tools are only built by promptrun.
func requiresRunner(spec api.Spec) bool {
	return workflowConfigured(spec.Workflow) || len(spec.Tools) > 0
}

// executeSyncRun runs the prompt in-process (CLI) — live output to stderr, final
// result returned — and persists the realized prompt for the launched session.
func executeSyncRun(ctx context.Context, rendered PromptRenderResult, opts AIPromptOptions) (PromptRunResult, error) {
	if len(rendered.Runtimes) > 0 || len(opts.MultiModels) > 0 {
		return executeSyncBatch(ctx, rendered, opts)
	}
	return executeSyncRunSingle(ctx, rendered, opts)
}

func executeSyncRunSingle(ctx context.Context, rendered PromptRenderResult, opts AIPromptOptions) (result PromptRunResult, err error) {
	ctx, terminal, err := attachPromptTerminal(ctx, &rendered, opts.NoStream)
	if err != nil {
		return PromptRunResult{}, err
	}
	if terminal != nil {
		defer func() { err = errors.Join(err, terminal.Close()) }()
	}
	groupOptions := []task.TaskGroupOption{
		task.WithKind("prompt"),
		task.WithLabels(promptTaskLabels(rendered, "")),
	}
	if terminal != nil {
		groupOptions = append(groupOptions, task.WithNoRender())
	}
	group := task.StartGroup[PromptRunResult]("prompt "+rendered.Name, groupOptions...)
	run := group.Add("execute "+rendered.Mode+":"+rendered.Model, func(_ flanksourceContext.Context, t *task.Task) (PromptRunResult, error) {
		taskCtx := ai.ContextWithLogger(t.Context(), t)
		return executeSyncRunSingleDirect(taskCtx, t, rendered, opts, nil)
	}, task.WithContext(ctx), task.WithModel(rendered.Model), task.WithPrompt(rendered.Input.Prompt.User))
	return run.GetResult()
}

func executeSyncRunSingleDirect(ctx context.Context, t *task.Task, rendered PromptRenderResult, opts AIPromptOptions, binding *promptSessionBinding) (PromptRunResult, error) {
	// A configured workflow or inline tools must go through the runner-backed
	// path: the direct provider call below never constructs verify/commit/judge
	// hooks or binds inline tools, so taking it would complete the run with the
	// declared checks skipped and the declared tools missing.
	if requiresRunner(rendered.Input) {
		return executeSyncWorkflowRun(t, rendered, opts.NoStream, binding)
	}
	r, err := executeDirectPrompt(ctx, rendered, opts)
	if err == nil {
		err = unansweredPromptQuestions(r.TerminalOutcome)
	}
	recording := promptRecordingInput{Rendered: rendered, Binding: binding}
	_, recordErr := recordCompletedRun(ctx, recording, directRun(rendered, r, err))
	result := PromptRunResult{
		Status:           "completed",
		Model:            r.Model,
		Provider:         r.Provider,
		Mode:             r.Mode,
		Text:             r.Text,
		StructuredOutput: r.StructuredOutput,
		SessionID:        r.SessionID,
		Dir:              r.Dir,
		HistoryFile:      r.HistoryFile,
		InputTokens:      r.InputTokens,
		OutputTokens:     r.Output,
		CostUSD:          r.CostUSD,
		Duration:         r.Duration,
	}
	if err != nil || recordErr != nil {
		result.Status = "failed"
	}
	return result, errors.Join(err, recordErr)
}

// executeDirectPrompt is one direct provider call: no runner, no hooks.
func executeDirectPrompt(ctx context.Context, rendered PromptRenderResult, opts AIPromptOptions) (AIPromptResult, error) {
	timeout, err := renderedTimeout(rendered)
	if err != nil {
		return AIPromptResult{}, err
	}
	out, err := executePromptRequestFunc(ctx, rendered.Input, rendered.Config, timeout, opts.NoStream)
	if out == nil {
		return AIPromptResult{}, err
	}
	result, ok := out.(AIPromptResult)
	if !ok {
		return AIPromptResult{}, fmt.Errorf("direct prompt execution returned %T, not AIPromptResult", out)
	}
	return result, err
}

// directRun is a direct provider call as promptrun files it: answered, or
// failed with the error that ended it.
func directRun(rendered PromptRenderResult, r AIPromptResult, err error) promptrun.Completed {
	done := promptrun.Completed{
		Resolved: promptResolution(rendered, rendered.Input),
		Runtime:  api.Runtime{Provider: r.Provider, Mode: api.RuntimeMode(r.Mode)},
		Model:    r.Model, ProviderSessionID: r.SessionID,
		Outcome: promptrun.Outcome{
			State: database.PromptRunStateSucceeded, Phase: database.PromptRunPhaseFinished,
			Text: r.Text, JSON: r.StructuredOutput,
		},
	}
	if err != nil {
		done.Outcome.State, done.Outcome.Phase, done.Outcome.Error = database.PromptRunStateFailed, database.PromptRunPhaseGenerate, err.Error()
	}
	return done
}

// executeSyncWorkflowRun executes a workflow-bearing CLI run through the same
// stream/runner machinery the server path uses, so hooks behave identically on
// both surfaces. A run whose verification fails returns an error: a declared
// check that does not pass must fail the command, not decorate its output.
func executeSyncWorkflowRun(t *task.Task, rendered PromptRenderResult, noStream bool, binding *promptSessionBinding) (PromptRunResult, error) {
	runID := uuid.NewString()
	stream := promptRuns.create(runID)
	// Nothing subscribes to a synchronous run's stream, and the broker's prune
	// loop only runs under `captain serve` — deregister so embedders don't
	// accumulate finished runs.
	defer promptRuns.remove(runID)
	timeout, err := renderedTimeout(rendered)
	if err != nil {
		return PromptRunResult{}, err
	}
	var summary PromptRunSummary
	if noStream {
		summary, err = runPromptBufferedWorkflow(t, rendered, timeout, runID, stream, binding)
	} else {
		summary, err = runPromptStream(t, rendered, timeout, runID, stream, binding)
	}
	result := PromptRunResult{
		Status:           "completed",
		RunID:            summary.RunID,
		Model:            summary.Model,
		Provider:         summary.Provider,
		Mode:             summary.Mode,
		Text:             summary.Text,
		StructuredOutput: summary.StructuredOutput,
		SessionID:        summary.SessionID,
		InputTokens:      summary.InputTokens,
		OutputTokens:     summary.OutputTokens,
		CostUSD:          summary.CostUSD,
		Duration:         summary.Duration,
	}
	if err != nil || !summary.Success {
		result.Status = "failed"
		if err == nil {
			err = errors.New(firstNonEmpty(summary.Error, "verification failed"))
		}
	}
	return result, err
}

func executeSyncBatch(ctx context.Context, rendered PromptRenderResult, opts AIPromptOptions) (PromptRunResult, error) {
	models := rendered.Runtimes
	if err := rendered.validateVariants(); err != nil {
		return PromptRunResult{}, err
	}
	if len(models) == 0 {
		if len(opts.MultiModels) > 0 {
			return PromptRunResult{}, errors.New("multi-model runtimes must be prepared by prompt rendering")
		}
		return executeSyncRunSingle(ctx, rendered, opts)
	}
	if len(models) == 1 {
		variant := renderVariant(rendered, rendered.variants[0])
		opts.MultiModels = nil
		start := time.Now()
		single, runErr := executeSyncRunSingle(ctx, variant, opts)
		item := PromptRunItem{
			Selector: runtimeSelector(models[0]), Model: firstNonEmpty(single.Model, models[0].Name),
			Provider: firstNonEmpty(single.Provider, providerName(models[0].Provider)),
			Mode:     firstNonEmpty(single.Mode, string(models[0].Mode)), Effort: string(models[0].Effort),
			Text: single.Text, SessionID: single.SessionID, Dir: single.Dir, HistoryFile: single.HistoryFile,
			InputTokens: single.InputTokens, OutputTokens: single.OutputTokens, CostUSD: single.CostUSD, Duration: single.Duration,
		}
		result := PromptRunResult{Total: 1, Runs: []PromptRunItem{item}, Duration: time.Since(start).Round(time.Millisecond).String()}
		if runErr != nil {
			result.Status, result.Failed, result.Runs[0].Status, result.Runs[0].Error = "failed", 1, "failed", runErr.Error()
			return result, runErr
		}
		result.Status, result.Succeeded, result.Runs[0].Status = "completed", 1, single.Status
		return result, nil
	}
	prepared := rendered.Input
	if err := resolvePromptAttachments(ctx, &prepared); err != nil {
		return PromptRunResult{}, err
	}
	rendered.Input.Prompt.Attachments = prepared.Prompt.Attachments
	if rendered.Input.SessionID != "" && len(models) > 1 {
		return PromptRunResult{}, errors.New("--resume cannot be used with multiple --multi-models variants")
	}
	if forced := api.RuntimeMode(strings.TrimSpace(opts.Mode)); forced != "" {
		for _, model := range models {
			if model.Mode != forced {
				return PromptRunResult{}, fmt.Errorf("--multi-models selector %s:%s conflicts with --mode %s", model.Mode, model.Name, forced)
			}
		}
	}

	start := time.Now()
	batch, err := createPromptBatchSessions(ctx, rendered, models)
	if err != nil {
		return PromptRunResult{}, err
	}
	updatePromptSessionLifecycle(ctx, batch.ID, database.SessionLifecycleRunning, "")
	runs := make([]PromptRunItem, len(models))
	group := task.StartGroup[PromptRunItem](
		"prompt "+rendered.Name,
		task.WithKind("prompt"),
		task.WithLabels(promptTaskLabels(rendered, "multi")),
		task.WithConcurrency(len(models)),
	)
	tasks := make([]task.TypedTask[PromptRunItem], len(models))
	for i, model := range models {
		i, model := i, model
		selector := string(model.Mode) + ":" + model.Name
		if model.Effort != api.EffortNone {
			selector += ":" + string(model.Effort)
		}
		tasks[i] = group.Add(selector, func(_ flanksourceContext.Context, t *task.Task) (PromptRunItem, error) {
			variant := renderVariant(rendered, rendered.variants[i])
			variantOpts := opts
			variantOpts.MultiModels = nil
			taskCtx := ai.ContextWithLogger(t.Context(), t)
			binding := promptBinding(batch, i)
			updatePromptSessionLifecycle(context.WithoutCancel(taskCtx), binding.SessionID, database.SessionLifecycleRunning, "")
			result, err := executeSyncRunSingleDirect(taskCtx, t, variant, variantOpts, binding)
			item := PromptRunItem{
				RunID:    binding.SessionID.String(),
				Selector: selector,
				Model:    model.Name,
				Provider: providerName(model.Provider),
				Mode:     string(model.Mode),
				Dir:      actualRunDir(variant.Input),
			}
			if err != nil {
				item.Status = "failed"
				item.HistoryFile = historyFileForRun(model.Provider, model.Mode, item.SessionID, item.Dir)
				item.Error = err.Error()
				return item, err
			}
			item.Status = result.Status
			item.Model = firstNonEmpty(result.Model, item.Model)
			item.Provider = firstNonEmpty(result.Provider, item.Provider)
			item.Mode = firstNonEmpty(result.Mode, item.Mode)
			item.Text = result.Text
			item.StructuredOutput = result.StructuredOutput
			providerSessionID := result.SessionID
			item.SessionID = binding.SessionID.String()
			item.Dir = firstNonEmpty(result.Dir, item.Dir)
			item.HistoryFile = firstNonEmpty(result.HistoryFile, historyFileForRun(providerOf(api.Runtime{Provider: item.Provider, Mode: api.RuntimeMode(item.Mode)}), api.RuntimeMode(item.Mode), providerSessionID, item.Dir))
			item.InputTokens = result.InputTokens
			item.OutputTokens = result.OutputTokens
			item.CostUSD = result.CostUSD
			item.Duration = result.Duration
			return item, nil
		}, task.WithModel(model.Name), task.WithPrompt(rendered.Input.Prompt.User))
	}
	for i, task := range tasks {
		item, err := task.GetResult()
		if err != nil && item.Error == "" {
			item.Status = "failed"
			item.Error = err.Error()
		}
		runs[i] = item
	}

	result := PromptRunResult{
		BatchID:  batch.ID.String(),
		Status:   "completed",
		Total:    len(runs),
		Runs:     runs,
		Duration: time.Since(start).Round(time.Millisecond).String(),
	}
	for _, run := range runs {
		if run.Error != "" || run.Status == "failed" {
			result.Failed++
			continue
		}
		result.Succeeded++
		result.InputTokens += run.InputTokens
		result.OutputTokens += run.OutputTokens
		result.CostUSD += run.CostUSD
	}
	switch {
	case result.Failed == 0:
		result.Status = "completed"
	case result.Succeeded == 0:
		result.Status = "failed"
	default:
		result.Status = "partial"
	}
	updatePromptSessionLifecycle(context.WithoutCancel(ctx), batch.ID,
		batchLifecycle(result.Succeeded, result.Failed),
		fmt.Sprintf("%d succeeded, %d failed", result.Succeeded, result.Failed))
	if result.Succeeded == 0 && result.Failed > 0 {
		return result, fmt.Errorf("all %d prompt variants failed", result.Failed)
	}
	return result, nil
}

func promptTaskLabels(rendered PromptRenderResult, mode string) map[string]string {
	labels := map[string]string{}
	if mode != "" {
		labels["mode"] = mode
	}
	if mode != "multi" {
		labels["model"] = rendered.Model
		labels["provider"] = rendered.Provider
		labels["mode"] = rendered.Mode
	}
	if rendered.Name != "" {
		labels["prompt"] = rendered.Name
	}
	if source := rendered.Input.Prompt.Source; source != "" {
		labels["source"] = source
	}
	if cwd := rendered.Input.Cwd(); cwd != "" {
		labels["cwd"] = cwd
	}
	return labels
}

func promptTaskLabelsWithID(rendered PromptRenderResult, id, mode string) map[string]string {
	labels := promptTaskLabels(rendered, mode)
	if id != "" {
		labels["promptId"] = id
	}
	return labels
}
