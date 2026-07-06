package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/clicky/task"
	flanksourceContext "github.com/flanksource/commons/context"
	"github.com/google/uuid"
)

// PromptRunHandle is the immediate response of the async "run" action: the run
// executes in the background as a clicky task; the client streams its session
// history from /api/captain/prompt/runs/{runId}/stream and its status from
// /api/captain/tasks.
type PromptRunHandle struct {
	RunID   string `json:"runId"`
	Status  string `json:"status"`
	Model   string `json:"model,omitempty"`
	Backend string `json:"backend,omitempty"`
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

// runPromptAction renders the prompt synchronously (so render/validation errors
// surface to the caller) then launches the model run in the background as a
// clicky task group and returns immediately with a handle to stream from.
func runPromptAction(ctx context.Context, id string, flags map[string]string) (PromptRunHandle, error) {
	req, err := readRenderRequest(ctx, flags)
	if err != nil {
		return PromptRunHandle{}, err
	}
	rendered, err := renderPrompt(ctx, id, req)
	if err != nil {
		return PromptRunHandle{}, err
	}
	if rendered.ValidationError != "" {
		return PromptRunHandle{}, errors.New(rendered.ValidationError)
	}

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

	return PromptRunHandle{RunID: runID, Status: "running", Model: rendered.Model, Backend: rendered.Backend}, nil
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
	if !ok {
		return failRun(t, stream, fmt.Errorf("backend %s does not support streaming", rendered.Backend))
	}

	start := time.Now()
	acc := newPromptEventAccumulator(stream.publish, t, rendered.Model, rendered.Backend)
	acc.cwd = req.Cwd()

	loop, err := ai.RunUntil(ctx, ai.LoopOptions{
		Provider:      streamer,
		MaxIterations: 1,
		BuildRequest: func(iter int, _ *ai.LoopIteration) (ai.Request, bool) {
			if iter > 0 {
				return ai.Request{}, false
			}
			return req, true
		},
		OnEvent: acc.handle,
	})
	if err != nil {
		return failRun(t, stream, err)
	}

	session := acc.sessionID
	if session == "" && len(loop.Iterations) > 0 {
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
	summary := PromptRunSummary{
		RunID:        runID,
		SessionID:    session,
		Model:        acc.model,
		Backend:      rendered.Backend,
		InputTokens:  acc.usage.InputTokens,
		OutputTokens: acc.usage.OutputTokens,
		CostUSD:      acc.cost,
		Duration:     time.Since(start).Round(time.Millisecond).String(),
		Success:      true,
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
