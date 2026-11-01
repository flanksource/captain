package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/middleware"
	"github.com/flanksource/captain/pkg/promptrun"
	"github.com/flanksource/clicky/task"
	"github.com/google/uuid"
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
	admitted := announceAdmittedSession(t, stream, rec, rendered.Model)
	req := rendered.Input
	relocated, err := preparePromptRun(ctx, &req, rendered.Config)
	if err != nil {
		_, recordErr := recordCompleted(ctx, rec, unstartedRun(rendered, err))
		return failRun(t, stream, errors.Join(err, recordErr))
	}
	if rec != nil {
		rec.Outcome = stoppedAsStopped(stream)
	}

	start := time.Now()
	acc := newPromptEventAccumulator(stream.publish, t, rendered.Model, rendered.Mode)
	acc.cwd, acc.idPrefix, acc.verify, acc.session = req.Cwd(), runID, stream.setVerify, stream.setRunMetadata
	onEvent := acc.handle
	if renderer := promptEventRenderer(ctx); renderer != nil && !noStream {
		onEvent = func(iteration int, event ai.Event) {
			acc.handle(iteration, event)
			renderer.Handle(iteration, event)
		}
	}
	if stream.observer != nil {
		previous := onEvent
		onEvent = func(iteration int, event ai.Event) {
			previous(iteration, event)
			stream.observer(iteration, event)
		}
	}
	result, err := promptrun.Run(ctx, promptrun.Input{
		Resolved:  promptResolution(rendered, req),
		Config:    rendered.Config,
		Relocated: relocated,
		OnEvent:   onEvent,
		Timeout:   timeout,
		NoStream:  noStream,
		Record:    rec,
	})
	stream.setRunMetadata(result.SessionID, firstNonEmpty(result.Model, rendered.Model))
	if err != nil {
		if result.Response == nil {
			return failRun(t, stream, err)
		}
		if stream.wasStopped() {
			err = errors.New("stopped")
		}
		summary, summaryErr := completedRunSummary(runID, rendered, result, binding, admitted, time.Since(start))
		if summaryErr != nil {
			err = errors.Join(err, summaryErr)
		}
		summary.Success, summary.Error = false, err.Error()
		stream.finish(&summary, err.Error())
		_, _ = t.FailedWithError(err)
		return summary, err
	}
	summary, err := completedRunSummary(runID, rendered, result, binding, admitted, time.Since(start))
	if err != nil {
		return failRun(t, stream, err)
	}
	stream.complete(summary)
	t.Success()
	return summary, nil
}

// announceAdmittedSession names the captain session a recorded run is admitted
// on — in the run frame and the task log — before anything is dispatched, so a
// follower can open it while a provider that reports no session of its own
// (an API run) is still working. It returns that id, or "" when the run is
// unrecorded or its admission session's id is assigned only on admission.
func announceAdmittedSession(t *task.Task, stream *runStream, rec *promptrun.Recording, model string) string {
	if rec == nil {
		return ""
	}
	id := rec.Placement.AdmittedSessionID()
	if id == uuid.Nil {
		return ""
	}
	stream.setRunMetadata(id.String(), model)
	t.Infof("session %s", id)
	return id.String()
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
// shared seam sees it: warn on a model name that looks mistyped, resolve the
// prompt's attachments against the local store, and — when the resolved sandbox
// executes elsewhere — return the factory for the provider that owns the whole
// run. A nil factory means the run executes here.
func preparePromptRun(ctx context.Context, req *ai.Request, cfg ai.Config) (func(ai.Config) (ai.Provider, error), error) {
	for _, c := range cfg.Model.Candidates() {
		warnIfLikelyModelTypo(c.Name)
	}
	if err := resolvePromptAttachments(ctx, req); err != nil {
		return nil, err
	}
	if !relocatesRun(cfg) {
		return nil, nil
	}
	return func(final ai.Config) (ai.Provider, error) { return remoteWorkflowProvider(req, final) }, nil
}

// completedRunSummary is the finished run as the CLI prints it and the stream
// reports it: the answer as text and as structured output, and the failure
// reason of a run whose checks said no. The session it names is the captain
// session when the run is bound to one, not the provider's own id; a run whose
// provider reports no session names the session it was admitted on.
func completedRunSummary(runID string, rendered PromptRenderResult, result promptrun.Result, binding *promptSessionBinding, admittedSessionID string, elapsed time.Duration) (PromptRunSummary, error) {
	structured, err := structuredOutputMap(result.StructuredData)
	if err != nil {
		return PromptRunSummary{}, err
	}
	questionErr := unansweredPromptQuestions(result.Response.TerminalOutcome)
	failure := promptrun.FailureReason(result.Verdicts)
	if questionErr != nil {
		failure = questionErr.Error()
	}
	text, err := structuredOutputText(result.Response.Text, structured)
	if err != nil {
		return PromptRunSummary{}, err
	}
	sessionID := firstNonEmpty(result.SessionID, admittedSessionID)
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
		Success:          result.Passed && questionErr == nil,
		Text:             text,
		StructuredOutput: structured,
		Error:            failure,
	}, nil
}

// remoteWorkflowProvider is the whole-run relocation branch: the resolved
// sandbox executes remotely, so the run happens on another machine and comes
// back whole. The provider owns the workspace (promptrun adds no setup hook)
// and never streams. cfg is the run's final config, so the caller tools it
// serves to the remote agent include the prompt's inline tools.
func remoteWorkflowProvider(req *ai.Request, cfg ai.Config) (ai.Provider, error) {
	remote, err := remoteExecProviderFor(req, cfg)
	if err != nil {
		return nil, err
	}
	if remote == nil {
		return nil, fmt.Errorf("sandbox %q relocates the run but built no remote provider", cfg.SandboxSelection.Kind)
	}
	wrapped, err := middleware.Wrap(remote, middleware.WithTracing(), middleware.WithLogging(), middleware.WithJevConversion(), middleware.WithSchemaValidation(cfg))
	if err != nil {
		closeProvider(remote)
		return nil, err
	}
	return bufferedOnlyProvider{Provider: wrapped}, nil
}

func failRun(t *task.Task, stream *runStream, err error) (PromptRunSummary, error) {
	if stream.wasStopped() {
		err = errors.New("stopped")
	}
	summary := stream.fail(err.Error())
	_, _ = t.FailedWithError(err)
	return summary, err
}
