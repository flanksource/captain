package middleware

import (
	"context"
	"fmt"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/tracing"
)

type tracingProvider struct {
	provider ai.Provider
}

func (t *tracingProvider) GetModel() string       { return t.provider.GetModel() }
func (t *tracingProvider) GetRuntime() ai.Runtime { return t.provider.GetRuntime() }
func (t *tracingProvider) Unwrap() ai.Provider    { return t.provider }

func (t *tracingProvider) chatOptions(req ai.Request) tracing.ChatOptions {
	mode, model := logRuntime(t.provider, req)
	return tracing.ChatOptions{
		Model:    model,
		Provider: t.provider.GetRuntime().Provider,
		Mode:     mode,
		Source:   req.Prompt.Source,
	}
}

func (t *tracingProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	ctx, chat := tracing.StartChat(ctx, t.chatOptions(req))
	resp, err := t.provider.Execute(ctx, req)
	chat.Response(resp)
	chat.End(err)
	return resp, err
}

// ExecuteStream forwards every event unchanged, folding each into the call's
// span, and ends the span when the inner stream closes. Once ctx is done the
// consumer may have stopped reading, so the rest of the stream is drained
// rather than forwarded, letting the provider finish closing it.
func (t *tracingProvider) ExecuteStream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	streamer, ok := t.provider.(ai.StreamingProvider)
	if !ok {
		return nil, fmt.Errorf("provider %s/%s does not support streaming", t.provider.GetRuntime(), t.provider.GetModel())
	}
	ctx, chat := tracing.StartChat(ctx, t.chatOptions(req))
	in, err := streamer.ExecuteStream(ctx, req)
	if err != nil {
		chat.End(err)
		return nil, err
	}
	out := make(chan ai.Event, cap(in))
	go func() {
		defer close(out)
		forwarding := true
		for ev := range in {
			chat.Observe(ev)
			if !forwarding {
				continue
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				forwarding = false
			}
		}
		chat.End(ctx.Err())
	}()
	return out, nil
}

// WithTracing opens an OpenTelemetry span per model call, with a child span per
// tool call the model makes.
func WithTracing() Option {
	return func(p ai.Provider) (ai.Provider, error) {
		return &tracingProvider{provider: p}, nil
	}
}
