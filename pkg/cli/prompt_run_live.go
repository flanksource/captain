package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/ai/agent/commit"
	"github.com/flanksource/captain/pkg/ai/agent/verify"
	"github.com/flanksource/clicky/task"
)

func runPromptStream(t *task.Task, rendered PromptRenderResult, timeout time.Duration, runID string, stream *runStream, binding *promptSessionBinding) (PromptRunSummary, error) {
	ctx, cancel := runContext(t.Context(), rendered.Input, timeout)
	stream.setCancel(cancel)
	defer cancel()
	ctx = ai.ContextWithLogger(ctx, t)

	req := rendered.Input
	cfg := rendered.Config
	if err := preparePromptAttachments(ctx, &req, cfg); err != nil {
		return failRun(t, stream, err)
	}
	p, cleanup, err := buildProvider(ctx, &req, cfg)
	if err != nil {
		return failRun(t, stream, err)
	}
	defer cleanup()
	defer closeProvider(p)

	streamer, ok := p.(ai.StreamingProvider)
	if !ok && !req.IsVerifyOnly() {
		return failRun(t, stream, fmt.Errorf("backend %s does not support streaming", rendered.Backend))
	}

	start := time.Now()
	acc := newPromptEventAccumulator(stream.publish, t, rendered.Model, rendered.Backend)
	acc.cwd = req.Cwd()
	acc.idPrefix = runID
	runner := &agent.Runner[string]{
		Provider: streamer,
		Request:  req,
		// Commit hooks lead so that at PhaseRun they squash before any teardown
		// hook (a worktree merge) runs and takes the result.
		Hooks:         append(commit.HooksForWorkflow(req.Workflow), verify.HooksForWorkflow(req.Workflow)...),
		MaxIterations: verify.MaxIterationsForWorkflow(req.Workflow),
		Repo:          req.Cwd(),
		Cwd:           req.Cwd(),
		Scope:         verify.ScopeForWorkflow(req.Workflow),
		OnEvent:       acc.handle,
	}
	runResult, err := runner.Run(ctx)
	if err != nil {
		if stream.wasStopped() {
			err = errors.New("stopped")
		}
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
	passed := verifyPassed(runResult.Verdicts)
	structuredOutput, err := structuredOutputMap(runResult.Response.StructuredData)
	if err != nil {
		return failRun(t, stream, err)
	}
	resultText, err := structuredOutputText(runResult.Response.Text, structuredOutput)
	if err != nil {
		return failRun(t, stream, err)
	}
	record := promptRunRecordInput{
		Rendered: rendered, RunID: runID, Binding: binding, SessionID: session,
		Model: acc.model, Backend: rendered.Backend, ResultText: resultText, ResultJSON: structuredOutput,
	}
	if !passed {
		record.Error = verifyReason(runResult.Verdicts)
	}
	persistPromptRun(context.WithoutCancel(ctx), record)
	summarySessionID := session
	if binding != nil {
		summarySessionID = binding.SessionID.String()
	}
	summary := PromptRunSummary{
		RunID:        runID,
		SessionID:    summarySessionID,
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
	if stream.wasStopped() {
		err = errors.New("stopped")
	}
	stream.fail(err.Error())
	_, _ = t.FailedWithError(err)
	return PromptRunSummary{Error: err.Error()}, err
}
