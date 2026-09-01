package api

import "context"

// Provider executes a single buffered model/agent call. Spec is the serializable
// request (model, prompt, budget, permissions, context, session).
type Provider interface {
	Execute(ctx context.Context, req Spec) (*Response, error)
	GetModel() string
	GetRuntime() Runtime
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

// ToolCapableProvider is implemented by providers that can expose and execute
// the caller-supplied Config.Tools (rather than only the backend's built-in
// tools). Resolve it with ProviderAs[ToolCapableProvider] to check support
// before relying on Config.Tools being honoured.
type ToolCapableProvider interface {
	SupportsCallerTools() bool
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
