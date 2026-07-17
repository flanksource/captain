package registry

import (
	"context"
	"sort"
	"sync"
)

// liveModelHooks are the optional live model-listing functions, keyed by
// provider name. pkg/ai installs them from its provider init(), mirroring the
// existing RegisterProvider/factories seam: the HTTP calls and key resolution
// they need live above this package, but the merged view belongs here.
var (
	liveModelMu    sync.RWMutex
	liveModelHooks = map[string]func(context.Context) ([]KnownModel, error){}
)

// RegisterLiveModels installs a live model-listing hook for a provider. Passing
// nil clears it. Registration is global and read by SupportedModels.
func RegisterLiveModels(provider string, fn func(context.Context) ([]KnownModel, error)) {
	liveModelMu.Lock()
	defer liveModelMu.Unlock()
	if fn == nil {
		delete(liveModelHooks, provider)
		return
	}
	liveModelHooks[provider] = fn
}

func (p *Provider) liveModels(ctx context.Context) ([]KnownModel, error) {
	liveModelMu.RLock()
	fn := liveModelHooks[p.Name]
	liveModelMu.RUnlock()
	if fn == nil {
		return nil, nil
	}
	return fn(ctx)
}

// SupportedModels returns the models this provider offers, as resolved Models
// carrying their backend, mode, and capabilities.
//
// The catalog snapshot is the floor: a live listing (when a hook is registered
// and the call succeeds) adds models the snapshot has not caught up with and
// refreshes what it knows, but a live failure degrades to the snapshot rather
// than emptying the list. Results are ordered newest-first.
//
// Models are reported on the provider's API mode by default; SupportedModelsFor
// answers for a specific mode.
func (p *Provider) SupportedModels(ctx context.Context) []Model {
	return p.SupportedModelsFor(ctx, ModeAPI)
}

// SupportedModelsFor returns the models this provider offers on one mode.
func (p *Provider) SupportedModelsFor(ctx context.Context, mode RuntimeMode) []Model {
	backend, err := p.BackendFor(mode)
	if err != nil {
		return nil
	}

	merged := map[string]KnownModel{}
	order := make([]string, 0)
	add := func(m KnownModel) {
		if !p.availableFor(m, mode) {
			return
		}
		if _, seen := merged[m.ID]; !seen {
			order = append(order, m.ID)
		}
		merged[m.ID] = m
	}
	for _, m := range p.Models() {
		if m.Preferred {
			add(m)
		}
	}
	// A live listing refines the snapshot; it never replaces it, so a provider
	// outage cannot empty the catalog.
	if live, err := p.liveModels(ctx); err == nil {
		for _, m := range live {
			if m.Provider == "" {
				m.Provider = p.Name
			}
			add(m)
		}
	}

	out := make([]Model, 0, len(order))
	for _, id := range order {
		out = append(out, Model{Name: id, Backend: backend}.Capabilities())
	}
	sort.SliceStable(out, func(i, j int) bool {
		return merged[out[i].Name].ReleaseDate > merged[out[j].Name].ReleaseDate
	})
	return out
}
