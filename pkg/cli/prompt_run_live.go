package cli

import (
	"context"
	"errors"
	"time"

	"github.com/flanksource/captain/pkg/ai"
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

	rec, err := promptRecording(ctx, promptRecordingInput{Rendered: rendered, RunID: runID, Binding: binding})
	if err != nil {
		return failRun(t, stream, err)
	}
	req := rendered.Input
	if err := preparePromptRun(ctx, &req, rendered.Config); err != nil {
		return failRun(t, stream, errors.Join(err, recordCompleted(ctx, rec, unstartedRun(rendered, err))))
	}
	if rec != nil {
		rec.Outcome = stoppedAsStopped(stream)
	}

	start := time.Now()
	acc := newPromptEventAccumulator(stream.publish, t, rendered.Model, rendered.Mode)
	acc.cwd, acc.idPrefix, acc.verify, acc.session = req.Cwd(), runID, stream.setVerify, stream.setRunMetadata
	result, err := promptrun.Run(ctx, promptrun.Input{
		Resolved: promptResolution(rendered, req),
		Config:   rendered.Config,
		OnEvent:  acc.handle,
		Timeout:  timeout,
		NoStream: noStream,
		Record:   rec,
	})
	stream.setRunMetadata(result.SessionID, firstNonEmpty(result.Model, rendered.Model))
	if err != nil {
		return failRun(t, stream, err)
	}
	summary, err := completedRunSummary(runID, rendered, result, binding, time.Since(start))
	if err != nil {
		return failRun(t, stream, err)
	}
	stream.complete(summary)
	t.Success()
	return summary, nil
}

// stoppedAsStopped files a run the stop button ended the way the stream
// reports it — "stopped" — rather than as the context error that ended it.
func stoppedAsStopped(stream *runStream) func(promptrun.Result, error, bool) (promptrun.Outcome, error) {
	return func(result promptrun.Result, runErr error, stopped bool) (promptrun.Outcome, error) {
		outcome, err := promptrun.DefaultOutcome(result, runErr, stopped)
		if stopped && stream.wasStopped() {
			outcome.Error = "stopped"
		}
		return outcome, err
	}
}

// preparePromptRun is everything this process must do to the request before the
// shared seam sees it: warn on a model name that looks mistyped and resolve the
// prompt's attachments against the local store. A resolved sandbox that executes
// elsewhere is promptrun's to relocate the run to.
func preparePromptRun(ctx context.Context, req *ai.Request, cfg ai.Config) error {
	for _, c := range cfg.Model.Candidates() {
		warnIfLikelyModelTypo(c.Name)
	}
	return resolvePromptAttachments(ctx, req)
}

// completedRunSummary is the finished run as the CLI prints it and the stream
// reports it: the answer as text and as structured output, and the failure
// reason of a run whose checks said no. The session it names is the captain
// session when the run is bound to one, not the provider's own id.
func completedRunSummary(runID string, rendered PromptRenderResult, result promptrun.Result, binding *promptSessionBinding, elapsed time.Duration) (PromptRunSummary, error) {
	structured, err := structuredOutputMap(result.StructuredData)
	if err != nil {
		return PromptRunSummary{}, err
	}
	text, err := structuredOutputText(result.Response.Text, structured)
	if err != nil {
		return PromptRunSummary{}, err
	}
	sessionID := result.SessionID
	if binding != nil {
		sessionID = binding.SessionID.String()
	}
	return PromptRunSummary{
		RunID:            runID,
		SessionID:        sessionID,
		Model:            firstNonEmpty(result.Model, rendered.Model),
		Provider:         rendered.Provider,
		Mode:             rendered.Mode,
		InputTokens:      result.Usage.InputTokens,
		OutputTokens:     result.Usage.OutputTokens,
		CostUSD:          result.CostUSD,
		Duration:         elapsed.Round(time.Millisecond).String(),
		Success:          result.Passed,
		Text:             text,
		StructuredOutput: structured,
		Error:            promptrun.FailureReason(result.Verdicts),
	}, nil
}

func failRun(t *task.Task, stream *runStream, err error) (PromptRunSummary, error) {
	if stream.wasStopped() {
		err = errors.New("stopped")
	}
	summary := stream.fail(err.Error())
	_, _ = t.FailedWithError(err)
	return summary, err
}
