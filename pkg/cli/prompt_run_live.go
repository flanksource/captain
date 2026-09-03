package cli

import (
	"context"
	"errors"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/middleware"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/promptrun"
	"github.com/flanksource/clicky/task"
)

func runPromptStream(t *task.Task, rendered PromptRenderResult, timeout time.Duration, runID string, stream *runStream, binding *promptSessionBinding) (PromptRunSummary, error) {
	return runPromptWorkflow(t, rendered, timeout, runID, stream, binding, false)
}

func runPromptBufferedWorkflow(t *task.Task, rendered PromptRenderResult, timeout time.Duration, runID string, stream *runStream, binding *promptSessionBinding) (PromptRunSummary, error) {
	return runPromptWorkflow(t, rendered, timeout, runID, stream, binding, true)
}

// runPromptWorkflow is `captain prompt run`'s caller of promptrun.Run: it owns
// what is specific to this process — the stop button, the live stream and its
// transcript frames, attachment resolution against the local store, the remote
// sandbox provider, and persistence — and hands the run itself to the shared
// seam so it means the same thing here as in an embedding host.
func runPromptWorkflow(t *task.Task, rendered PromptRenderResult, timeout time.Duration, runID string, stream *runStream, binding *promptSessionBinding, noStream bool) (PromptRunSummary, error) {
	ctx, cancel := context.WithCancel(t.Context())
	stream.setCancel(cancel)
	defer cancel()
	ctx = ai.ContextWithLogger(ctx, t)

	req := rendered.Input
	remote, err := preparePromptRun(ctx, &req, rendered.Config)
	if err != nil {
		return failRun(t, stream, err)
	}
	if remote != nil {
		defer closeProvider(remote)
	}

	start := time.Now()
	acc := newPromptEventAccumulator(stream.publish, t, rendered.Model, rendered.Mode)
	acc.cwd, acc.idPrefix, acc.verify = req.Cwd(), runID, stream.setVerify
	result, err := promptrun.Run(ctx, promptrun.Input{
		Request:  req,
		Config:   rendered.Config,
		Provider: remote,
		OnEvent:  acc.handle,
		Timeout:  timeout,
		NoStream: noStream,
	})
	stream.setRunMetadata(result.SessionID, firstNonEmpty(result.Model, rendered.Model))

	interrupted := contextEndedRun(ctx, err)
	record := promptRunRecord(rendered, runID, binding, result, interrupted)
	if err != nil {
		if stream.wasStopped() {
			err = errors.New("stopped")
		}
		persistPromptRun(context.WithoutCancel(ctx), failedRunRecord(record, err, interrupted))
		return failRun(t, stream, err)
	}
	structured, err := completeRunRecord(&record, result)
	if err != nil {
		persistPromptRun(context.WithoutCancel(ctx), failedRunRecord(record, err, false))
		return failRun(t, stream, err)
	}
	persistPromptRun(context.WithoutCancel(ctx), record)

	summary := completedRunSummary(record, result, binding, structured, time.Since(start))
	stream.complete(summary)
	t.Success()
	return summary, nil
}

// preparePromptRun is everything this process must do to the request before the
// shared seam sees it: warn on a model name that looks mistyped, resolve the
// prompt's attachments against the local store, and — when the resolved sandbox
// executes elsewhere — build the provider that owns the whole run. A nil
// provider means the run executes here.
func preparePromptRun(ctx context.Context, req *ai.Request, cfg ai.Config) (ai.Provider, error) {
	for _, c := range cfg.Model.Candidates() {
		warnIfLikelyModelTypo(c.Name)
	}
	if err := resolvePromptAttachments(ctx, req); err != nil {
		return nil, err
	}
	return remoteWorkflowProvider(req, cfg)
}

// promptRunRecord is the run as it will be persisted, assembled before the error
// branch so that an interrupted run — or one that broke on turn 2 of 3 — is
// still written down. Its verdict travels two ways: one row per turn (what was
// asked, what the check said, how long it took) and result_json.verify, the
// round's report beside the prompt's own structured output. Returning on the
// error path before this left a stopped run with no rows and no report at all.
func promptRunRecord(rendered PromptRenderResult, runID string, binding *promptSessionBinding, result promptrun.Result, interrupted bool) promptRunRecordInput {
	runtime := api.Runtime{Provider: rendered.Provider, Mode: api.RuntimeMode(rendered.Mode)}
	return promptRunRecordInput{
		Rendered: rendered, RunID: runID, Binding: binding, SessionID: result.SessionID,
		Model: firstNonEmpty(result.Model, rendered.Model), Provider: providerOf(runtime), Mode: runtime.Mode,
		ResultJSON: resultJSONWithVerify(nil, result.Report),
		Iterations: promptRunIterationRecords(result.Loop, result.Verdicts, interrupted),
	}
}

// completeRunRecord fills in what only a run that reached its own end has: the
// answer, as text and as structured output, and the failure reason of a run
// whose checks said no.
func completeRunRecord(record *promptRunRecordInput, result promptrun.Result) (map[string]any, error) {
	structured, err := structuredOutputMap(result.StructuredData)
	if err != nil {
		return nil, err
	}
	if record.ResultText, err = structuredOutputText(result.Response.Text, structured); err != nil {
		return nil, err
	}
	record.ResultJSON = resultJSONWithVerify(structured, result.Report)
	if !result.Passed {
		record.Error = promptrun.FailureReason(result.Verdicts)
	}
	return structured, nil
}

// completedRunSummary is the finished run as the CLI prints it and the stream
// reports it. The session it names is the captain session when the run is bound
// to one, not the provider's own id.
func completedRunSummary(record promptRunRecordInput, result promptrun.Result, binding *promptSessionBinding, structured map[string]any, elapsed time.Duration) PromptRunSummary {
	sessionID := record.SessionID
	if binding != nil {
		sessionID = binding.SessionID.String()
	}
	return PromptRunSummary{
		RunID:            record.RunID,
		SessionID:        sessionID,
		Model:            record.Model,
		Provider:         record.Rendered.Provider,
		Mode:             record.Rendered.Mode,
		InputTokens:      result.Usage.InputTokens,
		OutputTokens:     result.Usage.OutputTokens,
		CostUSD:          result.CostUSD,
		Duration:         elapsed.Round(time.Millisecond).String(),
		Success:          result.Passed,
		Text:             record.ResultText,
		StructuredOutput: structured,
		Error:            record.Error,
	}
}

// remoteWorkflowProvider is the whole-run relocation branch: when the resolved
// sandbox executes remotely, the run happens on another machine and comes back
// whole, so the provider it returns owns the workspace (promptrun adds no setup
// hook) and never streams. Nil means the run executes here.
func remoteWorkflowProvider(req *ai.Request, cfg ai.Config) (ai.Provider, error) {
	remote, err := remoteExecProviderFor(req, cfg)
	if err != nil || remote == nil {
		return nil, err
	}
	wrapped, err := middleware.Wrap(remote, middleware.WithLogging(), middleware.WithSchemaValidation(cfg))
	if err != nil {
		closeProvider(remote)
		return nil, err
	}
	return bufferedOnlyProvider{Provider: wrapped}, nil
}

// contextEndedRun reports whether the loop stopped because its context did —
// the stop button, or the run's own deadline. Both leave the last turn cut off
// rather than judged, and neither is the work's fault.
func contextEndedRun(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil
}

// failedRunRecord stamps the record of a run that ended in an error: an
// interrupted run is cancelled — its work was cut off, not judged — and anything
// else failed.
func failedRunRecord(record promptRunRecordInput, err error, interrupted bool) promptRunRecordInput {
	record.Error = err.Error()
	record.State = database.PromptRunStateFailed
	if interrupted {
		record.State = database.PromptRunStateCancelled
	}
	return record
}

func failRun(t *task.Task, stream *runStream, err error) (PromptRunSummary, error) {
	if stream.wasStopped() {
		err = errors.New("stopped")
	}
	summary := stream.fail(err.Error())
	_, _ = t.FailedWithError(err)
	return summary, err
}
