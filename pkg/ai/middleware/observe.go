package middleware

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons/merge"
)

type ProviderEvent struct {
	Phase          string
	Model, Backend string
	Iter           int
	Request        *ai.Request
	Response       *ai.Response
	Event          *ai.Event
	Usage          ai.Usage
	Cost           float64
	StartedAt      time.Time
	Duration       time.Duration
	Err            error
}

var providerObservers = struct {
	mu        sync.RWMutex
	next      uint64
	observers map[uint64]func(ProviderEvent)
	count     atomic.Int64
}{observers: make(map[uint64]func(ProviderEvent))}

// ObservingProvider reports whether any provider observer is registered.
func ObservingProvider() bool { return providerObservers.count.Load() != 0 }

// ObserveProvider registers a process-wide observer. Delivery is synchronous;
// the callback must not block. Provider calls are observed when a listener is
// registered at their start.
// Release is idempotent; a delivery already in progress may complete after it returns.
func ObserveProvider(deliver func(ProviderEvent)) (release func()) {
	if deliver == nil {
		panic("ObserveProvider: nil callback")
	}
	providerObservers.mu.Lock()
	providerObservers.next++
	id := providerObservers.next
	providerObservers.observers[id] = deliver
	providerObservers.count.Add(1)
	providerObservers.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			providerObservers.mu.Lock()
			delete(providerObservers.observers, id)
			providerObservers.count.Add(-1)
			providerObservers.mu.Unlock()
		})
	}
}

// PublishProvider delivers an event to the current observers.
func PublishProvider(event ProviderEvent) {
	if providerObservers.count.Load() == 0 {
		return
	}
	if event.Event != nil && event.Event.Kind == ai.EventTurnStart {
		if turn, ok := event.Event.Raw.(*ai.TurnStart); ok && turn != nil {
			eventCopy := *event.Event
			turnCopy := *turn
			turnCopy.Request = *redactedRequest(turn.Request)
			eventCopy.Raw = &turnCopy
			event.Event = &eventCopy
		}
	}
	providerObservers.mu.RLock()
	deliveries := make([]func(ProviderEvent), 0, len(providerObservers.observers))
	for _, deliver := range providerObservers.observers {
		deliveries = append(deliveries, deliver)
	}
	providerObservers.mu.RUnlock()
	for _, deliver := range deliveries {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					log.Errorf("provider observer panic: %v", recovered)
				}
			}()
			deliver(event)
		}()
	}
}

func providerObserverSnapshot() map[uint64]func(ProviderEvent) {
	providerObservers.mu.RLock()
	defer providerObservers.mu.RUnlock()
	observers := make(map[uint64]func(ProviderEvent), len(providerObservers.observers))
	for id, deliver := range providerObservers.observers {
		observers[id] = deliver
	}
	return observers
}

func publishObserved(observers map[uint64]func(ProviderEvent), event ProviderEvent) {
	providerObservers.mu.RLock()
	deliveries := make([]func(ProviderEvent), 0, len(observers))
	for id, deliver := range observers {
		if _, active := providerObservers.observers[id]; active {
			deliveries = append(deliveries, deliver)
		}
	}
	providerObservers.mu.RUnlock()
	for _, deliver := range deliveries {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					log.Errorf("provider observer panic: %v", recovered)
				}
			}()
			deliver(event)
		}()
	}
}

func redactedRequest(req ai.Request) *ai.Request {
	copy := merge.Clone(req, api.MergePolicy())
	if copy.Setup != nil {
		for i, env := range copy.Setup.Env {
			key, _, _ := strings.Cut(env, "=")
			copy.Setup.Env[i] = key + "=[REDACTED]"
		}
		for i := range copy.Setup.EnvVars {
			copy.Setup.EnvVars[i].ValueStatic = "[REDACTED]"
			copy.Setup.EnvVars[i].ValueFrom = nil
		}
	}
	return &copy
}

type observingProvider struct{ provider ai.Provider }

func (p *observingProvider) GetModel() string       { return p.provider.GetModel() }
func (p *observingProvider) GetRuntime() ai.Runtime { return p.provider.GetRuntime() }
func (p *observingProvider) Unwrap() ai.Provider    { return p.provider }

func WithObservers() Option {
	return func(p ai.Provider) (ai.Provider, error) { return &observingProvider{provider: p}, nil }
}

func (p *observingProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	if !ObservingProvider() {
		return p.provider.Execute(ctx, req)
	}
	return p.executeObserved(ctx, req)
}

func (p *observingProvider) executeObserved(ctx context.Context, req ai.Request) (*ai.Response, error) {
	start := time.Now()
	observers := providerObserverSnapshot()
	publishObserved(observers, ProviderEvent{Phase: "request", Model: p.GetModel(), Backend: p.GetRuntime().String(), Request: redactedRequest(req), StartedAt: start})
	resp, err := p.provider.Execute(ctx, req)
	if providerObservers.count.Load() != 0 {
		event := ProviderEvent{Phase: "response", Model: p.GetModel(), Backend: p.GetRuntime().String(), Response: resp, StartedAt: start, Duration: time.Since(start), Err: err}
		if resp != nil {
			event.Usage, event.Cost = resp.Usage, resp.CostUSD
		}
		publishObserved(observers, event)
	}
	return resp, err
}

func (p *observingProvider) ExecuteStream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	if !ObservingProvider() {
		streamer, ok := p.provider.(ai.StreamingProvider)
		if !ok {
			return nil, fmt.Errorf("provider %s/%s does not support streaming", p.GetRuntime(), p.GetModel())
		}
		return streamer.ExecuteStream(ctx, req)
	}
	return p.executeStreamObserved(ctx, req)
}

func (p *observingProvider) executeStreamObserved(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	start := time.Now()
	observers := providerObserverSnapshot()
	publishObserved(observers, ProviderEvent{Phase: "request", Model: p.GetModel(), Backend: p.GetRuntime().String(), Request: redactedRequest(req), StartedAt: start})
	streamer, ok := p.provider.(ai.StreamingProvider)
	var stream <-chan ai.Event
	var err error
	if ok {
		stream, err = streamer.ExecuteStream(ctx, req)
	} else {
		err = fmt.Errorf("provider %s/%s does not support streaming", p.GetRuntime(), p.GetModel())
	}
	if err != nil {
		publishObserved(observers, ProviderEvent{Phase: "response", Model: p.GetModel(), Backend: p.GetRuntime().String(), StartedAt: start, Duration: time.Since(start), Err: err})
		return nil, err
	}
	out := make(chan ai.Event, 1)
	go p.forwardStream(ctx, stream, out, start, observers)
	return out, nil
}

func (p *observingProvider) forwardStream(ctx context.Context, stream <-chan ai.Event, out chan<- ai.Event, start time.Time, observers map[uint64]func(ProviderEvent)) {
	response := &ai.Response{Model: p.GetModel(), Runtime: p.GetRuntime()}
	var text strings.Builder
	var streamErr error
	defer func() {
		response.Text, response.Duration = text.String(), time.Since(start)
		close(out)
		publishObserved(observers, ProviderEvent{Phase: "response", Model: p.GetModel(), Backend: p.GetRuntime().String(), Response: response, Usage: response.Usage, Cost: response.CostUSD, StartedAt: start, Duration: response.Duration, Err: streamErr})
	}()
	for ev := range stream {
		outcome, err := ai.TerminalOutcomeFromEvent(ev)
		if err != nil {
			streamErr = fmt.Errorf("invalid terminal outcome: %w", err)
		} else if outcome != nil {
			response.TerminalOutcome = outcome
		}
		switch ev.Kind {
		case ai.EventText:
			text.WriteString(ev.Text)
		case ai.EventResult:
			if text.Len() == 0 {
				text.WriteString(ev.Text)
			}
			if ev.Usage != nil {
				response.Usage = *ev.Usage
			}
			response.CostUSD, response.Context, response.Raw = ev.CostUSD, ev.Context, ev.Input
			if len(ev.StructuredData) > 0 {
				response.StructuredData = ev.StructuredData
			}
			if !ev.Success {
				streamErr = fmt.Errorf("provider result failed: %s", ev.Error)
			}
		case ai.EventError:
			streamErr = fmt.Errorf("provider stream: %s", ev.Error)
			if ev.Usage != nil {
				response.Usage = *ev.Usage
			}
			response.CostUSD = ev.CostUSD
		}
		select {
		case out <- ev:
		case <-ctx.Done():
			select {
			case out <- ev:
			default:
			}
		}
	}
	if streamErr == nil && ctx.Err() != nil {
		streamErr = ctx.Err()
	}
}
