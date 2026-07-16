package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	clickyapi "github.com/flanksource/clicky/api"
	clickyrpc "github.com/flanksource/clicky/rpc"
	"github.com/flanksource/clicky/task"
	flanksourceContext "github.com/flanksource/commons/context"
	"github.com/google/uuid"
)

// PromptRunResult is the unified result of the "run" action. Over HTTP (serve)
// it carries the async handle (RunID + Status "running") — the web UI then
// streams from /api/captain/prompt/runs/{runId}/stream. On the CLI it carries the
// synchronous result (Text + tokens/cost). One type serves both transports.
type PromptRunResult struct {
	RunID        string           `json:"runId,omitempty"`
	Status       string           `json:"status,omitempty" pretty:"label=Status"`
	Model        string           `json:"model,omitempty" pretty:"label=Model"`
	Backend      string           `json:"backend,omitempty" pretty:"label=Backend"`
	Chat         bool             `json:"chat,omitempty"`
	Capabilities ChatCapabilities `json:"capabilities,omitempty"`

	Text         string  `json:"text,omitempty" pretty:"label=Response"`
	SessionID    string  `json:"sessionId,omitempty" pretty:"label=Session"`
	Dir          string  `json:"dir,omitempty" pretty:"label=Dir"`
	HistoryFile  string  `json:"historyFile,omitempty" pretty:"label=History"`
	InputTokens  int     `json:"inputTokens,omitempty" pretty:"label=Input Tokens"`
	OutputTokens int     `json:"outputTokens,omitempty" pretty:"label=Output Tokens"`
	CostUSD      float64 `json:"costUSD,omitempty" pretty:"label=Cost USD"`
	Duration     string  `json:"duration,omitempty" pretty:"label=Duration"`

	Total     int             `json:"total,omitempty" pretty:"label=Total"`
	Succeeded int             `json:"succeeded,omitempty" pretty:"label=Succeeded"`
	Failed    int             `json:"failed,omitempty" pretty:"label=Failed"`
	Runs      []PromptRunItem `json:"runs,omitempty" pretty:"label=Runs"`
}

type PromptRunItem struct {
	Selector     string  `json:"selector,omitempty" pretty:"label=Selector"`
	Status       string  `json:"status,omitempty" pretty:"label=Status"`
	Model        string  `json:"model,omitempty" pretty:"label=Model"`
	Backend      string  `json:"backend,omitempty" pretty:"label=Backend"`
	Text         string  `json:"text,omitempty" pretty:"label=Response"`
	SessionID    string  `json:"sessionId,omitempty" pretty:"label=Session"`
	Dir          string  `json:"dir,omitempty" pretty:"label=Dir"`
	HistoryFile  string  `json:"historyFile,omitempty" pretty:"label=History"`
	InputTokens  int     `json:"inputTokens,omitempty" pretty:"label=Input Tokens"`
	OutputTokens int     `json:"outputTokens,omitempty" pretty:"label=Output Tokens"`
	CostUSD      float64 `json:"costUSD,omitempty" pretty:"label=Cost USD"`
	Duration     string  `json:"duration,omitempty" pretty:"label=Duration"`
	Error        string  `json:"error,omitempty" pretty:"label=Error"`
}

func (r PromptRunResult) Pretty() clickyapi.Text {
	if len(r.Runs) == 0 {
		if r.Text != "" {
			return clickyapi.Text{Content: r.Text}
		}
		return clickyapi.Text{Content: r.Status}
	}
	t := clickyapi.Text{}.
		Append(fmt.Sprintf("Status: %s  Total: %d  Succeeded: %d  Failed: %d  Duration: %s",
			r.Status, r.Total, r.Succeeded, r.Failed, r.Duration), "font-medium")

	t = t.NewLine().Add(promptRunComparisonTable(r.Runs))
	for _, run := range r.Runs {
		if strings.TrimSpace(run.Text) == "" {
			continue
		}
		t = t.NewLine().NewLine().
			Append("Response — ", "text-gray-500").
			Append(runColumnHeader(run), "font-bold").
			NewLine().
			Append(run.Text)
	}
	return t
}

func promptRunComparisonTable(runs []PromptRunItem) clickyapi.TextTable {
	table := clickyapi.TextTable{
		Headers:    clickyapi.TextList{textCell("Metric")},
		FieldNames: []string{"metric"},
	}
	for i, run := range runs {
		field := runColumnField(i)
		table.FieldNames = append(table.FieldNames, field)
		table.Headers = append(table.Headers, textCell(runColumnHeader(run)))
	}

	add := func(metric string, values func(PromptRunItem) string) {
		row := clickyapi.TableRow{"metric": cell(metric)}
		for i, run := range runs {
			row[runColumnField(i)] = cell(values(run))
		}
		table.Rows = append(table.Rows, row)
	}
	add("Status", func(run PromptRunItem) string { return run.Status })
	add("Backend", func(run PromptRunItem) string { return run.Backend })
	add("Model", func(run PromptRunItem) string { return run.Model })
	add("Error", func(run PromptRunItem) string { return truncateCell(run.Error, 160) })
	add("Duration", func(run PromptRunItem) string { return run.Duration })
	add("Tokens", func(run PromptRunItem) string { return tokenCell(run.InputTokens, run.OutputTokens) })
	add("Cost", func(run PromptRunItem) string { return costCell(run.CostUSD) })
	add("Session", func(run PromptRunItem) string { return shortSessionCell(run.SessionID) })
	add("History", func(run PromptRunItem) string { return truncatePathCell(run.HistoryFile, 72) })
	add("Dir", func(run PromptRunItem) string { return truncatePathCell(run.Dir, 56) })
	return table
}

func runColumnField(index int) string {
	return fmt.Sprintf("run%d", index+1)
}

func runColumnHeader(run PromptRunItem) string {
	if strings.TrimSpace(run.Selector) != "" {
		return run.Selector
	}
	if run.Backend != "" && run.Model != "" {
		return run.Backend + ":" + run.Model
	}
	return firstNonEmpty(run.Model, run.Backend, "run")
}

func textCell(s string) clickyapi.Textable {
	return clickyapi.Text{Content: s}
}

func cell(s string) clickyapi.TypedValue {
	return clickyapi.TypedValue{Textable: clickyapi.Text{Content: s}}
}

func truncateCell(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func truncatePathCell(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return "..." + s[len(s)-max+3:]
}

func shortSessionCell(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}

func tokenCell(input, output int) string {
	if input == 0 && output == 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d", input, output)
}

func costCell(cost float64) string {
	if cost <= 0 {
		return ""
	}
	return fmt.Sprintf("$%.4f", cost)
}

// PromptRunSummary is the terminal payload of a run: the SSE stream ends with it
// and the task's typed result carries it.
type PromptRunSummary struct {
	RunID        string  `json:"runId,omitempty"`
	SessionID    string  `json:"sessionId,omitempty"`
	Model        string  `json:"model,omitempty"`
	Backend      string  `json:"backend,omitempty"`
	InputTokens  int     `json:"inputTokens,omitempty"`
	OutputTokens int     `json:"outputTokens,omitempty"`
	CostUSD      float64 `json:"costUSD,omitempty"`
	Duration     string  `json:"duration,omitempty"`
	Success      bool    `json:"success"`
	Error        string  `json:"error,omitempty"`
}

// runPromptAction renders the prompt (from an id | .prompt filepath | --prompt |
// stdin), then executes it — synchronously on the CLI (returns the text + cost)
// or asynchronously over HTTP (returns a run handle to stream from). This is the
// single prompt-run implementation; `captain ai prompt` is a deprecated alias.
func runPromptAction(ctx context.Context, id string, flags map[string]string) (PromptRunResult, error) {
	_, isHTTP := clickyrpc.RequestFromContext(ctx)

	var rendered PromptRenderResult
	var opts AIPromptOptions
	var chatRequested bool
	if isHTTP {
		if strings.TrimSpace(flags["multi-models"]) != "" {
			return PromptRunResult{}, errors.New("--multi-models is only supported on the CLI prompt run path")
		}
		req, err := readRenderRequest(ctx, flags)
		if err != nil {
			return PromptRunResult{}, err
		}
		if rendered, err = renderPrompt(ctx, id, req); err != nil {
			return PromptRunResult{}, err
		}
		chatRequested = req.Chat
	} else {
		var err error
		if opts, err = actionFlagsToOptions(flags); err != nil {
			return PromptRunResult{}, err
		}
		if rendered, err = renderPromptCLI(ctx, id, opts, flags["vars"], readStdinIfCLI(ctx)); err != nil {
			return PromptRunResult{}, err
		}
	}
	if rendered.ValidationError != "" {
		return PromptRunResult{}, errors.New(rendered.ValidationError)
	}
	if chatRequested {
		if rendered.Input.Prompt.HasSchema() {
			return PromptRunResult{}, errors.New("chat mode does not support structured-output prompts")
		}
		if workflowConfigured(rendered.Input.Workflow) {
			return PromptRunResult{}, errors.New("chat mode does not support workflow-backed prompts")
		}
	}

	if isHTTP {
		return launchAsyncRun(id, rendered, chatRequested), nil
	}
	return executeSyncRun(ctx, rendered, opts)
}

var executePromptRequestFunc = executePromptRequest

// launchAsyncRun starts the background clicky task + SSE stream and returns the
// run handle (the serve/web-UI contract).
func launchAsyncRun(id string, rendered PromptRenderResult, chat bool) PromptRunResult {
	runID := uuid.NewString()
	stream := promptRuns.create(runID)
	timeout := runtimeTimeout(rendered.Input.Budget.Timeout)
	capabilities := chatCapabilitiesForBackend(rendered.Backend)
	stream.setRun(PromptRunFrame{
		RunID: runID, Status: "running", Chat: chat, Model: rendered.Model,
		Backend: rendered.Backend, Capabilities: capabilities,
	})

	group := task.StartGroup[PromptRunSummary](
		"prompt "+rendered.Name,
		task.WithGroupID(runID),
		task.WithKind("prompt"),
		task.WithLabels(promptTaskLabelsWithID(rendered, id, "")),
	)
	if chat {
		chatSession := newChatSession(runID, rendered, timeout, stream)
		promptChats.register(chatSession)
		group.Add("execute", func(_ flanksourceContext.Context, t *task.Task) (PromptRunSummary, error) {
			return chatSession.run(t)
		})
	} else {
		group.Add("execute", func(_ flanksourceContext.Context, t *task.Task) (PromptRunSummary, error) {
			return runPromptStream(t, rendered, timeout, runID, stream)
		})
	}
	return PromptRunResult{
		RunID: runID, Status: "running", Model: rendered.Model, Backend: rendered.Backend,
		Chat: chat, Capabilities: capabilities,
	}
}

func workflowConfigured(workflow *api.Workflow) bool {
	return workflow != nil && (workflow.Verify != nil || workflow.PostRun != nil || workflow.AutoVerifyWithoutFixture)
}

// executeSyncRun runs the prompt in-process (CLI) — live output to stderr, final
// result returned — and persists the realized prompt for the launched session.
func executeSyncRun(ctx context.Context, rendered PromptRenderResult, opts AIPromptOptions) (PromptRunResult, error) {
	if len(opts.MultiModels) > 0 {
		return executeSyncBatch(ctx, rendered, opts)
	}
	return executeSyncRunSingle(ctx, rendered, opts)
}

func executeSyncRunSingle(ctx context.Context, rendered PromptRenderResult, opts AIPromptOptions) (PromptRunResult, error) {
	group := task.StartGroup[PromptRunResult](
		"prompt "+rendered.Name,
		task.WithKind("prompt"),
		task.WithLabels(promptTaskLabels(rendered, "")),
	)
	run := group.Add("execute "+rendered.Backend+":"+rendered.Model, func(_ flanksourceContext.Context, t *task.Task) (PromptRunResult, error) {
		taskCtx := ai.ContextWithLogger(t.Context(), t)
		return executeSyncRunSingleDirect(taskCtx, rendered, opts, nil)
	}, task.WithModel(rendered.Model), task.WithPrompt(rendered.Input.Prompt.User))
	return run.GetResult()
}

func executeSyncRunSingleDirect(ctx context.Context, rendered PromptRenderResult, opts AIPromptOptions, batchID *uuid.UUID) (PromptRunResult, error) {
	out, err := executePromptRequestFunc(ctx, rendered.Input, rendered.Config, runtimeTimeout(rendered.Input.Budget.Timeout), opts.NoStream)
	if err != nil {
		return PromptRunResult{}, err
	}
	r, _ := out.(AIPromptResult)
	persistPromptRun(context.WithoutCancel(ctx), promptRunRecordInput{
		Rendered: rendered, SessionID: r.SessionID, Model: r.Model, Backend: r.Backend,
		BatchID: batchID, ResultText: r.Text,
	})
	return PromptRunResult{
		Status:       "completed",
		Model:        r.Model,
		Backend:      r.Backend,
		Text:         r.Text,
		SessionID:    r.SessionID,
		Dir:          r.Dir,
		HistoryFile:  r.HistoryFile,
		InputTokens:  r.InputTokens,
		OutputTokens: r.Output,
		CostUSD:      r.CostUSD,
		Duration:     r.Duration,
	}, nil
}

func executeSyncBatch(ctx context.Context, rendered PromptRenderResult, opts AIPromptOptions) (PromptRunResult, error) {
	models, err := ai.ResolveRuntimeSelectors(opts.MultiModels, rendered.Config.Model)
	if err != nil {
		return PromptRunResult{}, err
	}
	if len(models) == 0 {
		return executeSyncRunSingle(ctx, rendered, opts)
	}
	prepared := rendered.Input
	if err := resolvePromptAttachments(ctx, &prepared); err != nil {
		return PromptRunResult{}, err
	}
	rendered.Input.Prompt.Attachments = prepared.Prompt.Attachments
	if rendered.Input.SessionID != "" && len(models) > 1 {
		return PromptRunResult{}, errors.New("--resume cannot be used with multiple --multi-models variants")
	}
	if forced := api.Backend(strings.TrimSpace(opts.Backend)); forced != "" {
		for _, model := range models {
			if model.Backend != forced {
				return PromptRunResult{}, fmt.Errorf("--multi-models selector %s:%s conflicts with --backend %s", model.Backend, model.Name, forced)
			}
		}
	}

	start := time.Now()
	batchID := uuid.New()
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
		selector := string(model.Backend) + ":" + model.Name
		if model.Effort != api.EffortNone {
			selector += ":" + string(model.Effort)
		}
		tasks[i] = group.Add(selector, func(_ flanksourceContext.Context, t *task.Task) (PromptRunItem, error) {
			variant := renderVariant(rendered, model, fallbackModelsFromFlags(opts.Fallback))
			variantOpts := opts
			variantOpts.MultiModels = nil
			taskCtx := ai.ContextWithLogger(t.Context(), t)
			result, err := executeSyncRunSingleDirect(taskCtx, variant, variantOpts, &batchID)
			item := PromptRunItem{
				Selector: selector,
				Model:    model.Name,
				Backend:  string(model.Backend),
				Dir:      actualRunDir(variant.Input),
			}
			if err != nil {
				item.Status = "failed"
				item.HistoryFile = historyFileForRun(model.Backend, item.SessionID, item.Dir)
				item.Error = err.Error()
				return item, err
			}
			item.Status = result.Status
			item.Model = firstNonEmpty(result.Model, item.Model)
			item.Backend = firstNonEmpty(result.Backend, item.Backend)
			item.Text = result.Text
			item.SessionID = result.SessionID
			item.Dir = firstNonEmpty(result.Dir, item.Dir)
			item.HistoryFile = firstNonEmpty(result.HistoryFile, historyFileForRun(api.Backend(item.Backend), item.SessionID, item.Dir))
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
	if result.Succeeded == 0 && result.Failed > 0 {
		return result, fmt.Errorf("all %d prompt variants failed", result.Failed)
	}
	return result, nil
}

func promptTaskLabels(rendered PromptRenderResult, mode string) map[string]string {
	labels := map[string]string{
		"model":   rendered.Model,
		"backend": rendered.Backend,
	}
	if mode != "" {
		labels["mode"] = mode
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

func renderVariant(rendered PromptRenderResult, model api.Model, fallbacks []api.Model) PromptRenderResult {
	out := rendered
	req := rendered.Input
	cfg := rendered.Config
	req.Model = variantModel(req.Model, model, fallbacks)
	cfg.Model = variantModel(cfg.Model, model, fallbacks)
	out.Input = req
	out.Config = cfg
	out.Model = cfg.Model.Name
	out.Backend = string(cfg.Model.Backend)
	return out
}
