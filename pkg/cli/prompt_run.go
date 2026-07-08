package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/ai/agent/verify"
	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude"
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
	RunID   string `json:"runId,omitempty"`
	Status  string `json:"status,omitempty" pretty:"label=Status"`
	Model   string `json:"model,omitempty" pretty:"label=Model"`
	Backend string `json:"backend,omitempty" pretty:"label=Backend"`

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
	table := clickyapi.TextTable{
		Headers: clickyapi.TextList{
			textCell("Selector"),
			textCell("Status"),
			textCell("Backend"),
			textCell("Model"),
			textCell("Dir"),
			textCell("Session"),
			textCell("History"),
			textCell("Tokens"),
			textCell("Cost"),
			textCell("Duration"),
			textCell("Response"),
			textCell("Error"),
		},
		FieldNames: []string{"selector", "status", "backend", "model", "dir", "session", "history", "tokens", "cost", "duration", "response", "error"},
	}
	for _, run := range r.Runs {
		table.Rows = append(table.Rows, clickyapi.TableRow{
			"selector": cell(run.Selector),
			"status":   cell(run.Status),
			"backend":  cell(run.Backend),
			"model":    cell(run.Model),
			"dir":      cell(truncatePathCell(run.Dir, 56)),
			"session":  cell(shortSessionCell(run.SessionID)),
			"history":  cell(truncatePathCell(run.HistoryFile, 72)),
			"tokens":   cell(tokenCell(run.InputTokens, run.OutputTokens)),
			"cost":     cell(costCell(run.CostUSD)),
			"duration": cell(run.Duration),
			"response": cell(truncateCell(run.Text, 120)),
			"error":    cell(truncateCell(run.Error, 160)),
		})
	}
	return t.NewLine().Add(table)
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

	if isHTTP {
		return launchAsyncRun(id, rendered), nil
	}
	return executeSyncRun(ctx, rendered, opts)
}

var executePromptRequestFunc = executePromptRequest

// launchAsyncRun starts the background clicky task + SSE stream and returns the
// run handle (the serve/web-UI contract).
func launchAsyncRun(id string, rendered PromptRenderResult) PromptRunResult {
	runID := uuid.NewString()
	stream := promptRuns.create(runID)
	timeout := runtimeTimeout(rendered.Input.Budget.Timeout)

	group := task.StartGroup[PromptRunSummary](
		"prompt "+rendered.Name,
		task.WithGroupID(runID),
		task.WithKind("prompt"),
		task.WithLabels(promptTaskLabelsWithID(rendered, id, "")),
	)
	group.Add("execute", func(_ flanksourceContext.Context, t *task.Task) (PromptRunSummary, error) {
		return runPromptStream(t, rendered, timeout, runID, stream)
	})
	return PromptRunResult{RunID: runID, Status: "running", Model: rendered.Model, Backend: rendered.Backend}
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
		return executeSyncRunSingleDirect(taskCtx, rendered, opts)
	}, task.WithModel(rendered.Model), task.WithPrompt(rendered.Input.Prompt.User))
	return run.GetResult()
}

func executeSyncRunSingleDirect(ctx context.Context, rendered PromptRenderResult, opts AIPromptOptions) (PromptRunResult, error) {
	out, err := executePromptRequestFunc(ctx, rendered.Input, rendered.Config, runtimeTimeout(rendered.Input.Budget.Timeout), opts.NoStream)
	if err != nil {
		return PromptRunResult{}, err
	}
	r, _ := out.(AIPromptResult)
	if r.SessionID != "" {
		if st := sessionStore(); st != nil {
			st.upsertPrompt(StoredPrompt{
				SessionID: r.SessionID,
				Model:     r.Model,
				Backend:   r.Backend,
				Realized:  rendered,
			})
		}
	}
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
		tasks[i] = group.Add(selector, func(_ flanksourceContext.Context, t *task.Task) (PromptRunItem, error) {
			variant := renderVariant(rendered, model, fallbackModelsFromFlags(opts.Fallback))
			variantOpts := opts
			variantOpts.MultiModels = nil
			taskCtx := ai.ContextWithLogger(t.Context(), t)
			result, err := executeSyncRunSingleDirect(taskCtx, variant, variantOpts)
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

func historyFileForRun(backend api.Backend, sessionID, cwd string) string {
	if strings.TrimSpace(sessionID) == "" {
		return ""
	}
	switch backend {
	case api.BackendClaudeAgent, api.BackendClaudeCLI, api.BackendClaudeCmux:
		return claudeHistoryFile(sessionID, cwd)
	case api.BackendCodexAgent, api.BackendCodexCLI, api.BackendCodexCmux:
		return codexHistoryFile(sessionID)
	default:
		return ""
	}
}

func claudeHistoryFile(sessionID, cwd string) string {
	if cwd == "" {
		return ""
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return ""
	}
	return filepath.Join(claude.GetProjectsDir(), claude.NormalizePath(abs), sessionID+".jsonl")
}

func codexHistoryFile(sessionID string) string {
	files, err := history.FindCodexSessionFiles()
	if err != nil || len(files) == 0 {
		return ""
	}
	sort.Strings(files)
	for _, file := range files {
		if strings.TrimSuffix(filepath.Base(file), filepath.Ext(file)) == sessionID {
			return file
		}
		meta, err := history.ReadCodexSessionMeta(file)
		if err == nil && meta != nil && meta.ID == sessionID {
			return file
		}
	}
	for _, file := range files {
		if strings.HasPrefix(strings.TrimSuffix(filepath.Base(file), filepath.Ext(file)), sessionID) {
			return file
		}
	}
	return ""
}

func variantModel(base api.Model, model api.Model, fallbacks []api.Model) api.Model {
	out := base
	out.Name = model.Name
	out.ID = model.ID
	out.Backend = model.Backend
	out.Fallbacks = fallbacks
	return out
}

// runPromptStream drives a single streaming iteration, converting each ai.Event
// into a SessionEntry frame (published to stream) while driving the task's live
// status. It runs on clicky's worker goroutine and derives its context from the
// task (t.Context()), not the HTTP request, so the run outlives the POST.
func runPromptStream(t *task.Task, rendered PromptRenderResult, timeout time.Duration, runID string, stream *runStream) (PromptRunSummary, error) {
	ctx, cancel := runContext(t.Context(), rendered.Input, timeout)
	defer cancel()
	ctx = ai.ContextWithLogger(ctx, t)

	req := rendered.Input
	cfg := rendered.Config
	p, cleanup, err := buildProvider(ctx, &req, cfg)
	if err != nil {
		return failRun(t, stream, err)
	}
	defer cleanup()

	streamer, ok := p.(ai.StreamingProvider)
	if !ok && !req.IsVerifyOnly() {
		return failRun(t, stream, fmt.Errorf("backend %s does not support streaming", rendered.Backend))
	}

	start := time.Now()
	acc := newPromptEventAccumulator(stream.publish, t, rendered.Model, rendered.Backend)
	acc.cwd = req.Cwd()

	// Drive the generate→verify loop declared by spec.Workflow via the hook
	// runner: no verify ⇒ a single generation; verify commands re-run on failure
	// (feedback appended) up to maxIterations; a body-less spec verifies only.
	runner := &agent.Runner[string]{
		Provider:      streamer,
		Request:       req,
		Hooks:         verify.HooksForWorkflow(req.Workflow),
		MaxIterations: verify.MaxIterationsForWorkflow(req.Workflow),
		Repo:          req.Cwd(),
		Cwd:           req.Cwd(),
		Scope:         verify.ScopeForWorkflow(req.Workflow),
		OnEvent:       acc.handle,
	}
	runResult, err := runner.Run(ctx)
	if err != nil {
		return failRun(t, stream, err)
	}
	loop := runResult.Loop

	session := acc.sessionID
	if session == "" && runResult.Response.Workspace != nil {
		session = runResult.Response.Workspace.SessionID
	}
	if session == "" && loop != nil && len(loop.Iterations) > 0 {
		session = loop.Iterations[0].SessionID
	}
	// Persist the realized prompt for this launched session so `sessions get` can
	// show what produced it. External (non-captain) sessions have no such record.
	if session != "" {
		if st := sessionStore(); st != nil {
			st.upsertPrompt(StoredPrompt{
				SessionID: session,
				RunID:     runID,
				Model:     acc.model,
				Backend:   rendered.Backend,
				Realized:  rendered,
			})
		}
	}
	passed := verifyPassed(runResult.Verdicts)
	summary := PromptRunSummary{
		RunID:        runID,
		SessionID:    session,
		Model:        acc.model,
		Backend:      rendered.Backend,
		InputTokens:  acc.usage.InputTokens,
		OutputTokens: acc.usage.OutputTokens,
		CostUSD:      acc.cost,
		Duration:     time.Since(start).Round(time.Millisecond).String(),
		Success:      passed,
	}
	if !passed {
		summary.Error = verifyReason(runResult.Verdicts)
	}
	stream.complete(summary)
	t.Success()
	return summary, nil
}

func failRun(t *task.Task, stream *runStream, err error) (PromptRunSummary, error) {
	stream.fail(err.Error())
	_, _ = t.FailedWithError(err)
	return PromptRunSummary{Error: err.Error()}, err
}
