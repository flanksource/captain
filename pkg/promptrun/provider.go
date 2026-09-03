package promptrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flanksource/captain/pkg/ai"
)

// runnerProvider is the streaming face the runner drives. A streaming provider
// is used as-is; a buffered-only one — and every provider under NoStream — is
// wrapped so its completed response is replayed as the events the runner
// expects. A verify-only run has no provider to wrap and needs none.
func runnerProvider(provider ai.Provider, noStream, verifyOnly bool) (ai.StreamingProvider, error) {
	if provider == nil {
		if verifyOnly {
			return nil, nil
		}
		return nil, errors.New("promptrun: a generating run needs a provider")
	}
	if noStream {
		return bufferedProvider{Provider: provider}, nil
	}
	if streamer, ok := provider.(ai.StreamingProvider); ok {
		return streamer, nil
	}
	return bufferedProvider{Provider: provider}, nil
}

// bufferedProvider preserves the runner's event contract while forcing
// generation through Provider.Execute. It emits only completed-response events,
// so NoStream never invokes an underlying ExecuteStream.
type bufferedProvider struct {
	ai.Provider
}

func (p bufferedProvider) ExecuteStream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	resp, err := p.Execute(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, errors.New("buffered provider returned a nil response")
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
		return nil, fmt.Errorf("encode buffered structured output: %w", err)
	}
	if string(raw) == "null" {
		return nil, nil
	}
	return raw, nil
}
