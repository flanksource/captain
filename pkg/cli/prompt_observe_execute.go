package cli

import (
	"context"
	"errors"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/observation"
)

func executeObservationProvider(ctx context.Context, provider ai.Provider, req ai.Request, stream bool, recorder *observation.Recorder) (result observationRunResult) {
	start := time.Now()
	result = observationRunResult{model: provider.GetModel(), usedStream: stream}
	defer func() { result.durationMS = time.Since(start).Milliseconds() }()
	if !stream {
		response, err := provider.Execute(ctx, req)
		result.runtimeErr = err
		result.usage = recorder.Snapshot().Usage
		if response == nil {
			return
		}
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
	usageSnapshot := recorder.Snapshot()
	if usageSnapshot.UsageObserved {
		result.usage = usageSnapshot.Usage
	}
	return
}
