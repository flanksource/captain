package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/ai/agent/verify"
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
	InputTokens  int     `json:"inputTokens,omitempty" pretty:"label=Input Tokens"`
	OutputTokens int     `json:"outputTokens,omitempty" pretty:"label=Output Tokens"`
	CostUSD      float64 `json:"costUSD,omitempty" pretty:"label=Cost USD"`
	Duration     string  `json:"duration,omitempty" pretty:"label=Duration"`
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
		task.WithLabels(map[string]string{
			"promptId": id,
			"model":    rendered.Model,
			"backend":  rendered.Backend,
		}),
	)
	group.Add("execute", func(_ flanksourceContext.Context, t *task.Task) (PromptRunSummary, error) {
		return runPromptStream(t, rendered, timeout, runID, stream)
	})
	return PromptRunResult{RunID: runID, Status: "running", Model: rendered.Model, Backend: rendered.Backend}
}

// executeSyncRun runs the prompt in-process (CLI) — live output to stderr, final
// result returned — and persists the realized prompt for the launched session.
func executeSyncRun(ctx context.Context, rendered PromptRenderResult, opts AIPromptOptions) (PromptRunResult, error) {
	out, err := executePromptRequest(ctx, rendered.Input, rendered.Config, runtimeTimeout(rendered.Input.Budget.Timeout), opts.NoStream)
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
		InputTokens:  r.InputTokens,
		OutputTokens: r.Output,
		CostUSD:      r.CostUSD,
		Duration:     r.Duration,
	}, nil
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

	// Drive the generate→verify loop declared by spec.Workflow. With no verify,
	// this is a single generation (MaxIterations 1, no plugins); with verify
	// commands it re-runs on failure, appending feedback, up to maxIterations.
	// A verify-only spec (no body) leaves Build nil, so the runner skips
	// generation and only verifies the current state.
	var build func(*agent.RunContext, int, *ai.LoopIteration, string) ai.Request
	if !req.IsVerifyOnly() {
		build = func(_ *agent.RunContext, _ int, _ *ai.LoopIteration, feedback string) ai.Request {
			r := req
			if feedback != "" {
				r.Prompt.User = req.Prompt.User + "\n\n[verifier feedback]\n" + feedback + "\n\nFix the issues above and continue."
			}
			return r
		}
	}
	runner := &agent.Runner{
		Provider: streamer,
		Plugins:  verify.PluginsForWorkflow(req.Workflow),
		Loop: ai.LoopOptions{
			MaxIterations: verify.MaxIterationsForWorkflow(req.Workflow),
			OnEvent:       acc.handle,
		},
		Build: build,
		Repo:  req.Cwd(),
		Cwd:   req.Cwd(),
		Scope: verify.ScopeForWorkflow(req.Workflow),
	}
	runResult, err := runner.Run(ctx)
	if err != nil {
		return failRun(t, stream, err)
	}
	loop := runResult.Loop

	session := acc.sessionID
	if session == "" {
		session = runResult.SessionID
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
	passed := verdictsPassed(runResult.Verdicts, nil)
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
		summary.Error = verdictReason(runResult.Verdicts)
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
