package cli

import (
	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
)

// WhoamiOptions and AdapterStatus live in pkg/ai: the adapter probe moved there
// so non-CLI consumers (the prompt --schema builder and the aichat server's
// model menu) can reuse it and its caching without importing pkg/cli. They are
// aliased here for the `captain whoami` command and its renderer.
type WhoamiOptions = ai.WhoamiOptions
type AdapterStatus = ai.AdapterStatus

// WhoamiResult is the command's render model: the probed adapters plus
// display-only knobs consumed by Pretty(). The knobs are never serialized.
//
// Disabled carries the current opt-out set, Axes the universes it is drawn
// from, and Runtimes the provider×mode descriptor every picker renders from, so
// the whoami page can build every control from the one request it already makes
// instead of hardcoding the enums a second time.
type WhoamiResult struct {
	Adapters         []AdapterStatus                  `json:"adapters"`
	DefaultProvider  string                           `json:"defaultProvider"`
	ProviderDefaults map[string]ProviderDefaultView   `json:"providerDefaults"`
	Disabled         captainconfig.DisabledSelections `json:"disabled"`
	Axes             DisabledAxes                     `json:"axes"`
	Runtimes         []api.RuntimeFamily              `json:"runtimes"`

	sampleLimit int
	showModels  bool
}

// DisabledAxes enumerates every value each opt-out axis can hold. Backends are
// absent on purpose: the adapter list already carries them, one per card.
type DisabledAxes struct {
	Modes     []string `json:"modes"`
	Providers []string `json:"providers"`
	Efforts   []string `json:"efforts"`
}

func disabledAxes() DisabledAxes {
	axes := DisabledAxes{
		Modes:     make([]string, 0, len(api.AllRuntimeModes())),
		Providers: make([]string, 0, len(configurableProviders())),
		Efforts:   make([]string, 0, len(api.AllEfforts())),
	}
	for _, mode := range api.AllRuntimeModes() {
		axes.Modes = append(axes.Modes, string(mode))
	}
	for _, provider := range configurableProviders() {
		axes.Providers = append(axes.Providers, string(provider))
	}
	for _, effort := range api.AllEfforts() {
		axes.Efforts = append(axes.Efforts, string(effort))
	}
	return axes
}

func filterWhoamiModels(adapters []AdapterStatus, includeDisabled bool) []AdapterStatus {
	if includeDisabled {
		return adapters
	}

	filtered := make([]AdapterStatus, len(adapters))
	for i, adapter := range adapters {
		filtered[i] = adapter
		if len(adapter.ModelDetails) == 0 {
			continue
		}

		filtered[i].ModelCount = 0
		filtered[i].Models = nil
		filtered[i].ModelDetails = nil
		for _, model := range adapter.ModelDetails {
			if model.Disabled {
				continue
			}
			filtered[i].Models = append(filtered[i].Models, model.ID)
			filtered[i].ModelDetails = append(filtered[i].ModelDetails, model)
			filtered[i].ModelCount++
		}
	}
	return filtered
}

func RunWhoami(opts WhoamiOptions) (any, error) {
	adapters, err := ai.ProbeAdapters(opts, ai.OSAuthProbe())
	if err != nil {
		return nil, err
	}
	config, _, err := captainconfig.Load()
	if err != nil {
		return nil, err
	}
	// The command runs outside the server's lifecycle, so install the set here too
	// rather than relying on a PersistentPreRun that a library caller never hits.
	config.ApplyToRegistry()
	defaults, err := allProviderDefaults(config.AI)
	if err != nil {
		return nil, err
	}
	adapters = filterWhoamiModels(ai.ApplyDisabled(adapters), opts.IncludeDisabled)
	return WhoamiResult{
		Adapters: adapters, DefaultProvider: config.AI.ActiveProvider(),
		ProviderDefaults: defaults, Disabled: config.AI.Disabled, Axes: disabledAxes(),
		Runtimes:    api.RuntimeCatalog(),
		sampleLimit: opts.Limit, showModels: opts.Models,
	}, nil
}
