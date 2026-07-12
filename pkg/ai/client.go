package ai

import (
	"errors"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai/pricing"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/collections"
)

// The provider registry now lives in pkg/api (the stable runtime contract).
// ProviderFactory is re-exported as an alias; the registration/construction
// entrypoints are thin wrappers so existing call sites (and the blank-import
// self-registration in pkg/ai/provider) keep funneling into the single api
// registry unchanged.
type ProviderFactory = api.ProviderFactory

// RegisterProvider registers a factory for a backend in the shared api registry.
func RegisterProvider(backend Backend, factory ProviderFactory) {
	api.RegisterProvider(backend, factory)
}

// NewProvider constructs the registered provider for cfg's backend. When the model
// name is unrecognized, the error is enriched with the closest known model names
// ("did you mean …"). When cfg.Model resolves to more than one candidate (a
// comma-separated Name or a Fallbacks list), a fallback provider is returned that
// tries each in order on a fallback-eligible failure.
func NewProvider(cfg Config) (Provider, error) {
	resolved, err := ResolveModelSelectors(cfg.Model)
	if err != nil {
		return nil, err
	}
	cfg.Model = normalizeProviderModel(resolved)
	candidates := cfg.Model.Candidates()
	for i := range candidates {
		candidates[i] = normalizeProviderModel(candidates[i])
	}
	cfg.Model = candidates[0]
	if len(candidates) > 1 {
		return newFallbackProvider(cfg, candidates), nil
	}
	p, err := newResolvedProvider(cfg)
	if err != nil {
		return nil, suggestModelName(err, cfg.Model.Name)
	}
	return p, nil
}

func newResolvedProvider(cfg Config) (Provider, error) {
	p, err := api.NewProvider(cfg)
	if err != nil {
		return nil, err
	}
	return withModelErrorRecommendations(withEffortValidation(p, cfg.Model.Effort)), nil
}

func normalizeProviderModel(model api.Model) api.Model {
	backend := model.Backend
	if backend == "" {
		if inferred, err := api.InferBackend(model.Name); err == nil {
			backend = inferred
		}
	}
	if backend != "" {
		model.Name = NormalizeModelForBackend(backend, model.Name)
		model.Backend = backend
	}
	return model
}

// suggestModelName appends the closest catalog model ids to an unresolvable-model
// error, so e.g. "claud-sonnet-4" points at "claude-sonnet-4".
func suggestModelName(err error, model string) error {
	if model == "" || !errors.Is(err, api.ErrInferBackend) {
		return err
	}
	// Candidates are the catalog base names ("claude-sonnet-5"), which is the form
	// users type — the prefixed id ("anthropic/claude-sonnet-5") is far in edit
	// distance from a bare typo — plus the pricing registry's ids for broader
	// coverage (loaded from its disk cache; degrades to catalog-only if absent).
	var candidates []string
	for _, id := range modelIDsFrom(Catalog()) {
		if i := strings.LastIndex(id, "/"); i >= 0 {
			candidates = append(candidates, id[i+1:])
		} else {
			candidates = append(candidates, id)
		}
	}
	pricing.EnsureLoaded()
	for _, mi := range pricing.ListModels("") {
		candidates = append(candidates, mi.ModelID)
	}
	similar := collections.FindSimilar(model, candidates, 3)
	if len(similar) == 0 {
		return err
	}
	return fmt.Errorf("%w; did you mean: %s", err, strings.Join(similar, ", "))
}

// GetAPIKeyFromEnv returns the first non-empty value among a backend's auth env vars.
func GetAPIKeyFromEnv(backend Backend) string {
	return api.GetAPIKeyFromEnv(backend)
}
