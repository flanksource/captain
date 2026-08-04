package cli

import (
	"context"
	"encoding/json"
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
	return runPromptWorkflow(t, rendered, timeout, runID, stream, binding, false)
}

func runPromptBufferedWorkflow(t *task.Task, rendered PromptRenderResult, timeout time.Duration, runID string, stream *runStream, binding *promptSessionBinding) (PromptRunSummary, error) {
	return runPromptWorkflow(t, rendered, timeout, runID, stream, binding, true)
}

func runPromptWorkflow(t *task.Task, rendered PromptRenderResult, timeout time.Duration, runID string, stream *runStream, binding *promptSessionBinding, noStream bool) (PromptRunSummary, error) {
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

	streamer, err := workflowRunnerProvider(p, noStream, req.IsVerifyOnly())
	if err != nil {
		return failRun(t, stream, err)
	}

	judgeHooks, err := verify.PromptHooksForWorkflow(req.Workflow, p)
	if err != nil {
		return failRun(t, stream, err)
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
		Hooks:         append(append(commit.HooksForWorkflow(req.Workflow), verify.HooksForWorkflow(req.Workflow)...), judgeHooks...),
		MaxIterations: verify.MaxIterationsForWorkflow(req.Workflow),
		Repo:          req.Cwd(),
		Cwd:           req.Cwd(),
		Scope:         verify.ScopeForWorkflow(req.Workflow),
		OnEvent:       acc.handle,
	}
	runResult, err := runner.Run(ctx)
	session, model, usage, cost := acc.snapshot()
	loop := runResult.Loop
	if session == "" && runResult.Response.Workspace != nil {
		session = runResult.Response.Workspace.SessionID
	}
	if session == "" && loop != nil && len(loop.Iterations) > 0 {
		session = loop.Iterations[0].SessionID
	}
	stream.setRunMetadata(session, model)
	if err != nil {
		if stream.wasStopped() {
			err = errors.New("stopped")
		}
		return failRun(t, stream, err)
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
		Model: model, Backend: rendered.Backend, ResultText: resultText, ResultJSON: structuredOutput,
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
		RunID:            runID,
		SessionID:        summarySessionID,
		Model:            model,
		Backend:          rendered.Backend,
		InputTokens:      usage.InputTokens,
		OutputTokens:     usage.OutputTokens,
		CostUSD:          cost,
		Duration:         time.Since(start).Round(time.Millisecond).String(),
		Success:          passed,
		Text:             resultText,
		StructuredOutput: structuredOutput,
	}
	if !passed {
		summary.Error = verifyReason(runResult.Verdicts)
	}
	stream.complete(summary)
	t.Success()
	return summary, nil
}

func workflowRunnerProvider(provider ai.Provider, noStream, verifyOnly bool) (ai.StreamingProvider, error) {
	if noStream {
		return bufferedWorkflowProvider{Provider: provider}, nil
	}
	streamer, ok := provider.(ai.StreamingProvider)
	if !ok && !verifyOnly {
		return nil, fmt.Errorf("backend %s does not support streaming", provider.GetBackend())
	}
	return streamer, nil
}

// bufferedWorkflowProvider preserves the agent runner's event contract while
// forcing generation through Provider.Execute. It emits only completed response
// events, so --no-stream never invokes an underlying ExecuteStream method.
type bufferedWorkflowProvider struct {
	ai.Provider
}

func (p bufferedWorkflowProvider) ExecuteStream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	resp, err := p.Execute(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, errors.New("buffered workflow provider returned a nil response")
	}
	structured, err := bufferedStructuredData(resp.StructuredData)
	if err != nil {
		return nil, err
	}

	events := make(chan ai.Event, 3)
	if resp.Workspace != nil && resp.Workspace.SessionID != "" {
		events <- ai.Event{Kind: ai.EventSystem, SessionID: resp.Workspace.SessionID, Model: resp.Model}
	}
	if resp.Text != "" {
		events <- ai.Event{Kind: ai.EventText, Text: resp.Text, Model: resp.Model}
	}
	usage := resp.Usage
	events <- ai.Event{
		Kind: ai.EventResult, Success: true, Model: resp.Model, Usage: &usage,
		CostUSD: resp.CostUSD, StructuredData: structured, ToolApproval: resp.ToolApproval,
	}
	close(events)
	return events, nil
}

func bufferedStructuredData(value any) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	if raw, ok := value.(json.RawMessage); ok {
		return raw, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode buffered workflow structured output: %w", err)
	}
	if string(raw) == "null" {
		return nil, nil
	}
	return raw, nil
}

func failRun(t *task.Task, stream *runStream, err error) (PromptRunSummary, error) {
	if stream.wasStopped() {
		err = errors.New("stopped")
	}
	summary := stream.fail(err.Error())
	_, _ = t.FailedWithError(err)
	return summary, err
}
