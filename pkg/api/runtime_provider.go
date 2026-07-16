package api

import "context"

// Provider executes a single buffered model/agent call. Spec is the serializable
// request (model, prompt, budget, permissions, context, session).
type Provider interface {
	Execute(ctx context.Context, req Spec) (*Response, error)
	GetModel() string
	GetBackend() Backend
}

// StreamingProvider adds incremental streaming over the buffered Provider.
type StreamingProvider interface {
	Provider
	ExecuteStream(ctx context.Context, req Spec) (<-chan Event, error)
}

type ProviderUnwrapper interface {
	Unwrap() Provider
}

type InterruptibleProvider interface {
	Interrupt(context.Context) error
}

type SteerableProvider interface {
	Steer(context.Context, Spec) error
}

type CloseableProvider interface {
	Close() error
}

func ProviderAs[T any](provider Provider) (T, bool) {
	for provider != nil {
		if capability, ok := any(provider).(T); ok {
			return capability, true
		}
		wrapper, ok := provider.(ProviderUnwrapper)
		if !ok {
			break
		}
		provider = wrapper.Unwrap()
	}
	var zero T
	return zero, false
}
