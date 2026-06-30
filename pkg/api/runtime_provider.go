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
