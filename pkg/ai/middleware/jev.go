package middleware

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flanksource/captain/pkg/ai"
	native "github.com/flanksource/captain/pkg/ai/jev"
)

type jevConversionProvider struct {
	provider ai.Provider
}

func WithJevConversion() Option {
	return func(provider ai.Provider) (ai.Provider, error) {
		if provider.GetRuntime().Provider != "jev" {
			return provider, nil
		}
		return &jevConversionProvider{provider: provider}, nil
	}
}

func (p *jevConversionProvider) GetModel() string       { return p.provider.GetModel() }
func (p *jevConversionProvider) GetRuntime() ai.Runtime { return p.provider.GetRuntime() }
func (p *jevConversionProvider) Unwrap() ai.Provider    { return p.provider }

func (p *jevConversionProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	nativeReq, plan, err := prepareJev(req, p.provider.GetModel())
	if err != nil {
		return nil, err
	}
	response, err := p.provider.Execute(ctx, nativeReq)
	if err != nil {
		return response, err
	}
	if err := envelopeJev(response, plan, req); err != nil {
		return nil, err
	}
	return response, nil
}

func envelopeJev(response *ai.Response, plan jevPlan, original ai.Request) error {
	if response == nil {
		return fmt.Errorf("jev returned a nil response")
	}
	decoded, values, err := native.ParseResponse([]byte(response.Text), plan.request)
	if err != nil {
		return err
	}
	var value any = values
	if plan.scalar {
		value = values["value"]
	}
	if original.Prompt.Schema != nil {
		projected, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("jev encode projected value: %w", err)
		}
		if err := ai.BindStructuredOutput(original.Prompt.Schema, projected); err != nil {
			return err
		}
	}
	envelope, err := json.Marshal(map[string]any{"value": value, "typesafe": json.RawMessage(response.Text)})
	if err != nil {
		return fmt.Errorf("jev encode response envelope: %w", err)
	}
	response.Text = string(envelope)
	response.StructuredData = json.RawMessage(envelope)
	response.Raw = decoded
	return nil
}

func (p *jevConversionProvider) ExecuteStream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	streamer, ok := p.provider.(ai.StreamingProvider)
	if !ok {
		return nil, fmt.Errorf("jev provider does not support streaming")
	}
	nativeReq, plan, err := prepareJev(req, p.provider.GetModel())
	if err != nil {
		return nil, err
	}
	events, err := streamer.ExecuteStream(ctx, nativeReq)
	if err != nil {
		return nil, err
	}
	out := make(chan ai.Event)
	go func() {
		defer close(out)
		for event := range events {
			switch event.Kind {
			case ai.EventText:
				response := &ai.Response{Text: event.Text}
				if err := envelopeJev(response, plan, req); err != nil {
					out <- ai.Event{Kind: ai.EventError, Error: err.Error(), Raw: err}
					return
				}
				event.Text = response.Text
			case ai.EventResult:
				response := &ai.Response{Text: string(event.StructuredData)}
				if err := envelopeJev(response, plan, req); err != nil {
					out <- ai.Event{Kind: ai.EventError, Error: err.Error(), Raw: err}
					return
				}
				event.StructuredData = response.StructuredData.(json.RawMessage)
			}
			select {
			case out <- event:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}
