package ai

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons/logger"
)

// fallbackLog reports fallback transitions (a candidate being skipped or retried).
var fallbackLog = logger.GetLogger("ai")

// fallbackProvider tries an ordered list of candidate models, advancing to the
// next when the current one fails with a fallback-eligible error or its provider cannot be
// constructed. Providers are built lazily and cached. GetModel/GetRuntime report
// the active (last-selected) candidate. It implements StreamingProvider; a
// streaming candidate is abandoned in favour of the next only while it has not yet
// emitted any content (see ExecuteStream).
type fallbackProvider struct {
	base       Config      // shared Config; Model is swapped per candidate
	candidates []api.Model // ordered, len >= 2 (primary first)
	build      func(Config) (Provider, error)

	mu     sync.Mutex
	built  []Provider // lazily filled, len == len(candidates)
	active int
}

func newFallbackProvider(base Config, candidates []api.Model) *fallbackProvider {
	return &fallbackProvider{
		base:       base,
		candidates: candidates,
		build:      newResolvedProvider,
		built:      make([]Provider, len(candidates)),
	}
}

// cfgFor projects the shared Config onto candidate i. Non-primary candidates drop
// the primary's explicit APIKey so a fallback in another family resolves its own
// key from the environment (a --api-key meant for the primary must not be sent to
// a different provider).
func (f *fallbackProvider) cfgFor(i int) Config {
	cfg := f.base
	cfg.Model = f.candidates[i]
	if i > 0 {
		cfg.APIKey = ""
	}
	return cfg
}

// providerAt lazily constructs and caches the provider for candidate i.
func (f *fallbackProvider) providerAt(i int) (Provider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.built[i] != nil {
		return f.built[i], nil
	}
	p, err := f.build(f.cfgFor(i))
	if err != nil {
		return nil, suggestModelName(err, f.candidates[i].Name)
	}
	f.built[i] = p
	return p, nil
}

func (f *fallbackProvider) setActive(i int) {
	f.mu.Lock()
	f.active = i
	f.mu.Unlock()
}

func (f *fallbackProvider) GetModel() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.candidates[f.active].Name
}

func (f *fallbackProvider) GetRuntime() Runtime {
	f.mu.Lock()
	m := f.candidates[f.active]
	f.mu.Unlock()
	p, mode, err := m.Runtime()
	if err != nil {
		return Runtime{Mode: m.Mode}
	}
	return RuntimeOf(p, mode)
}

func (f *fallbackProvider) Unwrap() Provider {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.built[f.active]
}

func (f *fallbackProvider) Close() error {
	f.mu.Lock()
	built := append([]Provider(nil), f.built...)
	f.mu.Unlock()
	var errs []error
	for _, provider := range built {
		if closer, ok := api.ProviderAs[api.CloseableProvider](provider); ok {
			errs = append(errs, closer.Close())
		}
	}
	return errors.Join(errs...)
}

// Execute runs the primary and, on a fallback-eligible failure or a construction error,
// each fallback in turn. A non-retryable runtime error stops immediately (another
// model will not fix a malformed request). When nothing succeeds the primary's
// error is returned, as the most actionable one.
func (f *fallbackProvider) Execute(ctx context.Context, req Request) (*Response, error) {
	if err := ValidateAttachmentCompatibility(f.candidates, req.Prompt.Attachments); err != nil {
		return nil, err
	}
	log := LoggerFromContext(ctx, fallbackLog)
	var firstErr error
	record := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	for i := range f.candidates {
		p, err := f.providerAt(i)
		if err != nil {
			logFallbackTransition(log, f.candidates, i, err)
			record(err)
			continue
		}
		f.setActive(i)
		candidateReq := req
		candidateReq.Effort = f.candidates[i].Effort
		resp, err := p.Execute(ctx, candidateReq)
		if err == nil {
			return resp, nil
		}
		record(err)
		if !IsFallbackEligible(err) {
			return resp, err
		}
		if i < len(f.candidates)-1 {
			log.Warnf("fallback: model %s failed (%v); trying %s",
				f.candidates[i].Name, err, f.candidates[i+1].Name)
		}
	}
	return nil, firstErr
}

// ExecuteStream streams the first candidate that produces output. A candidate is
// abandoned for the next only while it has not yet emitted a content event: an
// EventError (or a silent close) before any content advances to the next model;
// once content has been forwarded the stream is committed and any later error is
// surfaced to the caller.
func (f *fallbackProvider) ExecuteStream(ctx context.Context, req Request) (<-chan Event, error) {
	if err := ValidateAttachmentCompatibility(f.candidates, req.Prompt.Attachments); err != nil {
		return nil, err
	}
	out := make(chan Event)
	go func() {
		defer close(out)
		log := LoggerFromContext(ctx, fallbackLog)
		var firstErr error
		record := func(err error) {
			if firstErr == nil {
				firstErr = err
			}
		}
		emit := func(err error) { sendEvent(ctx, out, Event{Kind: EventError, Error: err.Error()}) }

		for i := range f.candidates {
			last := i == len(f.candidates)-1

			p, err := f.providerAt(i)
			if err != nil {
				logFallbackTransition(log, f.candidates, i, err)
				record(err)
				continue
			}
			sp, ok := p.(StreamingProvider)
			if !ok {
				record(fmt.Errorf("model %s runtime does not support streaming", f.candidates[i].Name))
				continue
			}
			f.setActive(i)

			candidateReq := req
			candidateReq.Effort = f.candidates[i].Effort
			ch, err := sp.ExecuteStream(ctx, candidateReq)
			if err != nil {
				record(err)
				eligible := IsFallbackEligible(err)
				if eligible && !last {
					log.Warnf("fallback: model %s failed (%v); trying %s",
						f.candidates[i].Name, err, f.candidates[i+1].Name)
					continue
				}
				if eligible && firstErr != nil {
					err = firstErr
				}
				emit(err)
				return
			}

			committed, streamErr := f.pump(ctx, ch, out)
			if committed {
				return // the committed candidate's events (incl. any terminal error) are already forwarded
			}
			if streamErr == nil {
				streamErr = fmt.Errorf("model %s produced no output", f.candidates[i].Name)
			}
			record(streamErr)
			eligible := IsFallbackEligible(streamErr)
			if eligible && !last {
				log.Warnf("fallback: model %s failed pre-output (%v); trying %s",
					f.candidates[i].Name, streamErr, f.candidates[i+1].Name)
				continue
			}
			if eligible && firstErr != nil {
				streamErr = firstErr
			}
			emit(streamErr)
			return
		}
		if firstErr != nil {
			emit(firstErr)
		}
	}()
	return out, nil
}

func logFallbackTransition(log logger.Logger, candidates []api.Model, i int, err error) {
	if i < len(candidates)-1 {
		log.Warnf("fallback: model %s unavailable (%v); trying %s", candidates[i].Name, err, candidates[i+1].Name)
		return
	}
	log.Warnf("fallback: model %s unavailable (%v)", candidates[i].Name, err)
}

// pump forwards a candidate's stream to out. Leading non-content events (e.g.
// EventSystem session metadata) are buffered until the first content event, at
// which point the buffer is flushed and the stream is "committed" — from then on
// every event, including a terminal error, is forwarded. An EventError (or a
// silent close) before any content leaves the stream uncommitted so the caller can
// try the next candidate; the buffered leading events of a discarded candidate are
// dropped, never forwarded. It returns whether the stream committed and, when it
// did not, the failure that ended it (nil on a silent close).
func (f *fallbackProvider) pump(ctx context.Context, ch <-chan Event, out chan<- Event) (bool, error) {
	var buffered []Event
	committed := false
	for ev := range ch {
		if committed {
			if !sendEvent(ctx, out, ev) {
				return true, nil
			}
			continue
		}
		switch {
		case ev.Kind == EventError:
			return false, errors.New(ev.Error)
		case isCommittingEvent(ev.Kind):
			committed = true
			for _, b := range buffered {
				if !sendEvent(ctx, out, b) {
					return true, nil
				}
			}
			buffered = nil
			if !sendEvent(ctx, out, ev) {
				return true, nil
			}
		default:
			buffered = append(buffered, ev)
		}
	}
	return committed, nil
}

// isCommittingEvent reports whether an event carries content the user has already
// seen, past which point falling back to another model would double up output.
func isCommittingEvent(k EventKind) bool {
	switch k {
	case EventText, EventThinking, EventToolUse, EventToolResult, EventResult, EventPermission:
		return true
	default:
		return false
	}
}

// sendEvent forwards one event unless ctx is done, returning false when the send
// was abandoned so callers stop pumping instead of leaking on a gone consumer.
func sendEvent(ctx context.Context, out chan<- Event, ev Event) bool {
	select {
	case out <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}
