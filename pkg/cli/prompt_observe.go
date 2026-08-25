package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/observation"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/clicky"
	clickyrpc "github.com/flanksource/clicky/rpc"
	"github.com/flanksource/commons/logger"
	"github.com/google/uuid"
)

type PromptObserveFlags struct {
	PromptActionFlags
	Runtime string `flag:"runtime" help:"Exactly one runtime selector, e.g. api:gpt-5.6-sol:high" required:"true"`
}

func (PromptObserveFlags) ClickyActionFlags() {}

type observationRunResult struct {
	usage      *api.Usage
	costUSD    float64
	model      string
	runtimeErr error
	terminal   bool
	usedStream bool
	durationMS int64
}

func observePromptAction(ctx context.Context, id string, flags map[string]string) (api.RuntimeObservation, error) {
	if _, isHTTP := clickyrpc.RequestFromContext(ctx); isHTTP {
		return api.RuntimeObservation{}, errors.New("prompt observe v1 is CLI-only and does not support async HTTP execution")
	}
	forceObservationOutput()

	selector := strings.TrimSpace(flags["runtime"])
	if selector == "" {
		return api.RuntimeObservation{}, errors.New("--runtime is required")
	}
	opts, err := actionFlagsToOptions(flags)
	if err != nil {
		return api.RuntimeObservation{}, err
	}
	if len(opts.MultiModels) > 0 {
		return api.RuntimeObservation{}, errors.New("prompt observe v1 does not support --multi-models")
	}
	if len(opts.Fallback) > 0 {
		return api.RuntimeObservation{}, errors.New("prompt observe v1 does not support fallback chains")
	}
	for _, name := range []string{"model", "backend", "mode"} {
		if strings.TrimSpace(flags[name]) != "" {
			return api.RuntimeObservation{}, fmt.Errorf("--%s cannot be combined with --runtime in prompt observe v1", name)
		}
	}
	// Seed the renderer so --runtime works without saved defaults.
	opts.Model = selector

	rendered, err := renderPromptCLI(ctx, id, opts, flags["vars"], readStdinIfCLI(ctx))
	if err != nil {
		return api.RuntimeObservation{}, err
	}
	if rendered.ValidationError != "" {
		return api.RuntimeObservation{}, errors.New(rendered.ValidationError)
	}
	if len(rendered.Runtimes) > 0 {
		return api.RuntimeObservation{}, errors.New("prompt observe v1 does not support prompt runtime matrices")
	}
	if len(rendered.Config.Model.Fallbacks) > 0 || len(rendered.Input.Fallbacks) > 0 {
		return api.RuntimeObservation{}, errors.New("prompt observe v1 does not support fallback chains")
	}
	if workflowConfigured(rendered.Input.Workflow) {
		return api.RuntimeObservation{}, errors.New("prompt observe v1 does not support workflow-backed prompts")
	}

	runtimes, err := ai.ResolveRuntimeSelectors([]string{selector}, rendered.Config.Model)
	if err != nil {
		return api.RuntimeObservation{}, err
	}
	if len(runtimes) != 1 {
		return api.RuntimeObservation{}, fmt.Errorf("--runtime must resolve to exactly one runtime, got %d", len(runtimes))
	}
	runtime := runtimes[0]
	if err := runtime.Validate(); err != nil {
		return api.RuntimeObservation{}, fmt.Errorf("invalid --runtime: %w", err)
	}
	runtime = runtime.Capabilities()

	requestedEffort := requestedObservationEffort(selector, flags["effort"])
	resolvedEffort, unsupported := resolveObservationEffort(runtime)
	result := newRuntimeObservation(selector, runtime, requestedEffort, resolvedEffort)
	applyObservationCaptureRequest(&result, rendered.Input)
	if unsupported != "" {
		result.Availability = api.Available()
		result.Execution = api.ObservationExecution{
			State: "not_started",
			Error: &api.ObservationError{Code: unsupported, Message: "the resolved runtime does not support the requested reasoning effort"},
		}
		result.Controls.ReasoningEffort.Resolved = api.ObservationStringFact{
			State: api.ObservationFactUnsupported, ReasonCode: unsupported,
		}
		result.Controls.ReasoningEffort.Observed = api.ObservationStringFact{
			State: api.ObservationFactUnsupported, ReasonCode: unsupported,
		}
		result.Capture.Dispatch.Status = api.ObservationCaptureComplete
		result.Capture.Permissions.Status = api.ObservationCaptureComplete
		result.Capture.Tools.Status = api.ObservationCaptureComplete
		return checkedObservation(result)
	}
	runtime.Effort = resolvedEffort
	rendered = renderVariant(rendered, runtime, nil)
	rendered.Input.Fallbacks = nil
	rendered.Config.Model.Fallbacks = nil

	recorder := observation.NewRecorder()
	runCtx, cancel := runContext(
		observation.ContextWithRecorder(ctx, recorder),
		rendered.Input,
		remoteAwareTimeout(rendered.Input, rendered.Config, runtimeTimeout(opts.Timeout)),
	)
	defer cancel()

	req := rendered.Input
	cfg := rendered.Config
	cfg.CanUseTool = recorder.PermissionBroker(cfg.CanUseTool)
	if err := preparePromptAttachments(runCtx, &req, cfg); err != nil {
		return api.RuntimeObservation{}, err
	}

	provider, cleanup, err := buildProvider(runCtx, &req, cfg)
	if err != nil {
		if failure, ok := unavailableObservationFailure(err); ok {
			result.Availability = failure.availability
			result.Execution = api.ObservationExecution{State: "not_started", Error: failure.observationError}
			applyObservationSnapshot(&result, recorder.Snapshot(), false, relocatesRun(cfg))
			return checkedObservation(result)
		}
		return api.RuntimeObservation{}, err
	}
	defer cleanup()
	defer closeProvider(provider)

	remote := relocatesRun(cfg)
	stream := runtime.Streaming && !opts.NoStream && !req.Prompt.HasSchema() && !remote
	run := executeObservationProvider(runCtx, provider, req, stream, recorder)
	duration := run.durationMS
	result.Execution.DurationMS = &duration
	result.Metrics.DurationMS = api.ObservationNumberFact{State: api.ObservationFactKnown, Value: &duration, Unit: "ms"}
	applyObservationSnapshot(&result, recorder.Snapshot(), run.usedStream, remote)
	applyObservationMetrics(&result, runtime.Backend, firstNonEmpty(run.model, runtime.Name), run.usage, run.costUSD)
	if run.runtimeErr != nil || !run.terminal {
		failure := runtimeObservationFailure(run.runtimeErr, run.terminal)
		result.Availability = failure.availability
		result.Execution.State = failure.executionState
		result.Execution.Error = failure.observationError
	} else {
		result.Availability = api.Available()
		result.Execution.State = "completed"
		result.Execution.Error = nil
	}
	return checkedObservation(result)
}

func forceObservationOutput() {
	clicky.Flags.FormatOptions = clicky.FormatOptions{Format: "json"}
	clicky.Flags.Level = "fatal"
	clicky.Flags.LevelCount = 0
	clicky.Flags.LogToStderr = true
	clicky.Flags.UseFlags()
	// Provider logs can contain prompts or credential-bearing endpoints.
	logger.SetOutput(io.Discard)
}

func requestedObservationEffort(selector, flagValue string) api.ObservationStringFact {
	parts := strings.Split(selector, ":")
	if len(parts) > 1 {
		if effort := api.Effort(strings.TrimSpace(parts[len(parts)-1])); effort != api.EffortNone && effort.Valid() {
			value := string(effort)
			return api.ObservationStringFact{State: api.ObservationFactKnown, Value: &value}
		}
	}
	if effort := api.Effort(strings.TrimSpace(flagValue)); effort != api.EffortNone && effort.Valid() {
		value := string(effort)
		return api.ObservationStringFact{State: api.ObservationFactKnown, Value: &value}
	}
	return api.ObservationStringFact{State: api.ObservationFactUnset}
}

func resolveObservationEffort(runtime api.Model) (api.Effort, string) {
	effort := runtime.Effort
	if effort == api.EffortNone {
		return effort, ""
	}
	supported, _, known := registry.ModelEfforts(runtime.Backend, runtime.Name)
	if known && (len(supported) == 0 || !slices.Contains(supported, effort)) {
		return api.EffortNone, "reasoning_effort_unsupported"
	}
	resolved, err := registry.ResolveEffort(runtime.Backend, runtime.Name, effort)
	if err != nil || resolved == api.EffortNone {
		return api.EffortNone, "reasoning_effort_unsupported"
	}
	return resolved, ""
}

func newRuntimeObservation(selector string, runtime api.Model, requested api.ObservationStringFact, effort api.Effort) api.RuntimeObservation {
	provider := ""
	if owner, _, ok := registry.ProviderFor(runtime.Backend); ok {
		provider = owner.Name
	}
	resolved := api.ObservationStringFact{State: api.ObservationFactUnset}
	if effort != api.EffortNone {
		value := string(effort)
		resolved = api.ObservationStringFact{State: api.ObservationFactKnown, Value: &value}
	}
	return api.RuntimeObservation{
		SchemaVersion: api.ObservationSchemaV1,
		ObservationID: uuid.NewString(),
		Runtime: api.ObservationRuntime{
			Requested: api.ObservationRuntimeRequested{Selector: selector},
			Resolved: api.ObservationRuntimeResolved{
				Provider: provider, Backend: runtime.Backend, Mode: runtime.Mode, Model: runtime.Name,
			},
		},
		Availability: api.Available(),
		Execution:    api.ObservationExecution{State: "not_started", Error: nil},
		Controls: api.ObservationControls{ReasoningEffort: api.ObservationControl{
			Requested: requested,
			Resolved:  resolved,
			Observed: api.ObservationStringFact{
				State: api.ObservationFactUnknown, ReasonCode: "dispatch_not_observed",
			},
		}},
		Capture: api.ObservationCapture{
			Dispatch:    api.ObservationDispatchCapture{Status: dispatchCaptureStatus(runtime.Backend), Events: []api.ObservationDispatchEvent{}},
			Permissions: api.ObservationPermissionCapture{Status: permissionCaptureStatus(runtime.Backend), Events: []api.ObservationPermissionEvent{}},
			Tools:       api.ObservationToolCapture{Status: api.ObservationCaptureUnavailable, Events: []api.ObservationToolEvent{}},
			MCP:         api.ObservationExternalCapture{Status: api.ObservationCaptureUnsupported, Events: []api.ObservationExternalEvent{}},
			Kubernetes:  api.ObservationExternalCapture{Status: api.ObservationCaptureUnsupported, Events: []api.ObservationExternalEvent{}},
		},
		Metrics: api.ObservationMetrics{
			DurationMS: api.ObservationNumberFact{State: api.ObservationFactUnknown, Unit: "ms"},
			CostUSD:    api.ObservationCostFact{State: api.ObservationFactUnknown, Unit: "USD"},
			Usage:      api.ObservationUsageFact{State: api.ObservationFactUnknown, Semantics: "disjoint-v1"},
		},
		Artifacts: []api.ObservationArtifact{},
	}
}

func executeObservationProvider(ctx context.Context, provider ai.Provider, req ai.Request, stream bool, recorder *observation.Recorder) (result observationRunResult) {
	start := time.Now()
	result = observationRunResult{model: provider.GetModel(), usedStream: stream}
	defer func() { result.durationMS = time.Since(start).Milliseconds() }()
	if !stream {
		response, err := provider.Execute(ctx, req)
		result.runtimeErr = err
		if response == nil {
			return
		}
		usage := response.Usage
		result.usage = &usage
		result.costUSD = response.CostUSD
		result.model = firstNonEmpty(response.Model, result.model)
		result.terminal = err == nil
		return
	}

	streamer, ok := provider.(ai.StreamingProvider)
	if !ok {
		result.runtimeErr = errors.New("resolved provider does not expose streaming")
		return
	}
	events, err := streamer.ExecuteStream(ctx, req)
	if err != nil {
		result.runtimeErr = err
		return
	}
	for event := range events {
		recorder.RecordEvent(event)
		if event.Model != "" {
			result.model = event.Model
		}
		switch event.Kind {
		case ai.EventError:
			result.runtimeErr = errors.New(firstNonEmpty(event.Error, "provider emitted an error event"))
		case ai.EventResult:
			result.terminal = event.Success
			if event.Usage != nil {
				usage := *event.Usage
				result.usage = &usage
			}
			result.costUSD = event.CostUSD
			if !event.Success && result.runtimeErr == nil {
				result.runtimeErr = errors.New("provider reported an unsuccessful result")
			}
		}
	}
	if ctx.Err() != nil && result.runtimeErr == nil {
		result.runtimeErr = ctx.Err()
	}
	return
}

func applyObservationSnapshot(result *api.RuntimeObservation, snapshot observation.Snapshot, streamed, remote bool) {
	result.Capture.Dispatch.Events = nonNilObservationEvents(snapshot.Dispatch)
	result.Capture.Permissions.Events = nonNilObservationEvents(snapshot.Permissions)
	result.Capture.Tools.Events = nonNilObservationEvents(snapshot.Tools)
	if remote {
		result.Capture.Dispatch.Status = api.ObservationCaptureUnavailable
		result.Capture.Permissions.Status = api.ObservationCaptureUnavailable
		result.Capture.Tools.Status = api.ObservationCaptureUnavailable
	} else if streamed {
		result.Capture.Tools.Status = api.ObservationCaptureComplete
	}
	if snapshot.Overflow {
		result.Capture.Dispatch.Status = partialIfComplete(result.Capture.Dispatch.Status)
		result.Capture.Permissions.Status = partialIfComplete(result.Capture.Permissions.Status)
		result.Capture.Tools.Status = partialIfComplete(result.Capture.Tools.Status)
	}
	if snapshot.DispatchPartial {
		result.Capture.Dispatch.Status = partialIfComplete(result.Capture.Dispatch.Status)
	}
	if len(snapshot.Dispatch) > 0 {
		result.Controls.ReasoningEffort.Observed = snapshot.Effort
	} else {
		switch result.Capture.Dispatch.Status {
		case api.ObservationCaptureUnsupported:
			result.Controls.ReasoningEffort.Observed = api.ObservationStringFact{
				State: api.ObservationFactUnknown, ReasonCode: "dispatch_instrumentation_unsupported",
			}
		case api.ObservationCaptureUnavailable:
			result.Controls.ReasoningEffort.Observed = api.ObservationStringFact{
				State: api.ObservationFactUnknown, ReasonCode: "dispatch_instrumentation_unavailable",
			}
		default:
			result.Controls.ReasoningEffort.Observed = snapshot.Effort
		}
	}
}

func applyObservationCaptureRequest(result *api.RuntimeObservation, req ai.Request) {
	if req.Permissions.MCP.Disabled {
		result.Capture.MCP.Status = api.ObservationCaptureNotRequested
	}
}

func applyObservationMetrics(result *api.RuntimeObservation, backend api.Backend, model string, usage *api.Usage, reportedCost float64) {
	if usage == nil {
		return
	}
	result.Metrics.Usage = api.ObservationUsageFact{
		State: api.ObservationFactKnown, Semantics: "disjoint-v1",
		Buckets: &api.ObservationUsageBuckets{
			InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
			ReasoningTokens: usage.ReasoningTokens, CacheReadTokens: usage.CacheReadTokens,
			CacheWriteTokens: usage.CacheWriteTokens,
		},
	}
	if reportedCost > 0 && providerReportsCost(backend) {
		value := reportedCost
		result.Metrics.CostUSD = api.ObservationCostFact{
			State: api.ObservationFactKnown, Value: &value, Unit: "USD", Source: "provider",
		}
		return
	}
	estimate := ai.PriceUsage(backend, model, *usage, 0).Total()
	if estimate > 0 {
		result.Metrics.CostUSD = api.ObservationCostFact{
			State: api.ObservationFactKnown, Value: &estimate, Unit: "USD", Source: "captain_estimated",
		}
	}
}

func providerReportsCost(backend api.Backend) bool {
	return backend == api.BackendClaudeCLI || backend == api.BackendClaudeAgent
}

type observationFailure struct {
	availability     api.Availability
	executionState   string
	observationError *api.ObservationError
}

func unavailableObservationFailure(err error) (observationFailure, bool) {
	failure := classifyObservationFailure(err)
	return failure, failure.availability.State != api.AvailabilityAvailable
}

func runtimeObservationFailure(err error, terminal bool) observationFailure {
	if err == nil && !terminal {
		return observationFailure{
			availability: api.Available(), executionState: "failed",
			observationError: &api.ObservationError{Code: "incomplete_event_stream", Message: "the runtime ended without a terminal result"},
		}
	}
	failure := classifyObservationFailure(err)
	if failure.executionState == "" {
		failure.executionState = "failed"
	}
	return failure
}

func classifyObservationFailure(err error) observationFailure {
	failure := observationFailure{availability: api.Available(), executionState: "failed"}
	set := func(code, message string) observationFailure {
		failure.observationError = &api.ObservationError{Code: code, Message: message}
		return failure
	}
	switch {
	case err == nil:
		return set("runtime_failure", "the runtime did not complete")
	case ai.IsMissingAPIKey(err):
		failure.availability.State = api.AvailabilityMissingCredential
		failure.executionState = "not_started"
		return set("missing_credentials", "the runtime is missing required credentials")
	case errors.Is(err, ai.ErrCLINotFound):
		failure.availability.State = api.AvailabilityMissingExecutable
		failure.executionState = "not_started"
		return set("missing_executable", "the runtime executable is unavailable")
	case ai.IsModelUnavailable(err):
		failure.availability.State = api.AvailabilityUnavailable
		failure.executionState = "not_started"
		return set("model_unavailable", "the resolved model is unavailable")
	case errors.Is(err, ai.ErrTimeout), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return set("timeout", "the runtime execution timed out")
	case errors.Is(err, ai.ErrBudgetExceeded):
		return set("budget_exceeded", "the runtime exceeded its configured budget")
	case errors.Is(err, ai.ErrSchemaValidation):
		return set("schema_validation_failed", "the runtime response failed schema validation")
	}
	switch ai.ClassifyError(err) {
	case registry.ErrorAuth:
		failure.availability.State = api.AvailabilityNotAuthenticated
		failure.executionState = "not_started"
		return set("authentication_failed", "the runtime could not authenticate")
	case registry.ErrorRateLimit:
		return set("rate_limited", "the provider rate limited the runtime")
	case registry.ErrorOverloaded:
		return set("provider_overloaded", "the provider was overloaded")
	case registry.ErrorNetwork:
		return set("network_failure", "the runtime encountered a network failure")
	case registry.ErrorContextLength:
		return set("context_length_exceeded", "the prompt exceeded the runtime context window")
	case registry.ErrorInvalidRequest:
		return set("provider_invalid_request", "the provider rejected the runtime request")
	case registry.ErrorModelUnavailable:
		failure.availability.State = api.AvailabilityUnavailable
		failure.executionState = "not_started"
		return set("model_unavailable", "the resolved model is unavailable")
	default:
		return set("runtime_failure", "the runtime failed")
	}
}

func dispatchCaptureStatus(backend api.Backend) api.ObservationCaptureStatus {
	switch backend {
	case api.BackendOpenAI, api.BackendCodexCLI, api.BackendCodexAgent:
		return api.ObservationCaptureComplete
	default:
		return api.ObservationCaptureUnsupported
	}
}

func permissionCaptureStatus(backend api.Backend) api.ObservationCaptureStatus {
	switch backend {
	case api.BackendAnthropic, api.BackendOpenAI, api.BackendGemini, api.BackendDeepSeek, api.BackendClaudeAgent:
		return api.ObservationCaptureComplete
	case api.BackendCodexAgent, api.BackendClaudeCmux, api.BackendCodexCmux:
		return api.ObservationCapturePartial
	default:
		return api.ObservationCaptureUnsupported
	}
}

func partialIfComplete(status api.ObservationCaptureStatus) api.ObservationCaptureStatus {
	if status == api.ObservationCaptureComplete {
		return api.ObservationCapturePartial
	}
	return status
}

func nonNilObservationEvents[T any](events []T) []T {
	if events == nil {
		return []T{}
	}
	return events
}

func checkedObservation(result api.RuntimeObservation) (api.RuntimeObservation, error) {
	if _, err := json.Marshal(result); err != nil {
		return api.RuntimeObservation{}, fmt.Errorf("serialize observation: %w", err)
	}
	return result, nil
}
