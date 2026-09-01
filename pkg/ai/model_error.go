package ai

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/collections"
)

const modelRecommendationLimit = 5
const modelRecommendationTimeout = 2 * time.Second

// modelAvailabilityResolver is replaceable in tests so model-error handling
// never needs a real provider account or installed CLI.
var modelAvailabilityResolver = availableModelsForRuntime

// availableModelsForRuntime returns models executable by the selected runtime.
// Codex's installed catalog is the strongest source for its local adapters; the
// API mode uses its authenticated model endpoint. The embedded registry keeps
// recommendations useful when discovery is unavailable.
func availableModelsForRuntime(ctx context.Context, p *ModelProvider, mode RuntimeMode) []ModelDef {
	discoveryCtx, cancel := context.WithTimeout(ctx, modelRecommendationTimeout)
	defer cancel()
	if p == OpenAI && mode.Kind() == "cli" {
		if binary, err := exec.LookPath("codex"); err == nil {
			if models, err := FetchCodexDebugModels(discoveryCtx, binary); err == nil && len(models) > 0 {
				for i := range models {
					models[i].Provider = p.Name
				}
				return CurrentCuratedModelsByReleaseDate(models)
			}
		}
	}
	if mode.Kind() == "api" && GetAPIKeyFromEnv(p, mode) != "" {
		if models, err := ListModels(discoveryCtx, p); err == nil && len(models) > 0 {
			return CurrentModelsByReleaseDate(models)
		}
	}
	return CurrentCuratedModelsByReleaseDate(RegistryModelDefs(p, mode))
}

// recommendModelError preserves err while adding a runtime-scoped replacement
// and a compact available-model list. It runs only after a provider has
// confirmed that the attempted model is unavailable.
func recommendModelError(ctx context.Context, p *ModelProvider, mode RuntimeMode, attempted string, err error) error {
	if !IsModelUnavailable(err) || strings.Contains(strings.ToLower(err.Error()), "available models for") {
		return err
	}
	return recommendModelErrorFromModels(p, mode, attempted, err, modelAvailabilityResolver(ctx, p, mode))
}

// recommendModelErrorFromModels names the whole runtime, not just the family:
// the list it reports is what this provider offers on this mode, and the two
// modes of one provider do not offer the same models.
func recommendModelErrorFromModels(p *ModelProvider, mode RuntimeMode, attempted string, err error, models []ModelDef) error {
	available := make([]string, 0, len(models))
	seen := map[string]bool{}
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		if id == "" || strings.EqualFold(id, attempted) || seen[id] {
			continue
		}
		seen[id] = true
		available = append(available, id)
	}
	if len(available) == 0 {
		return err
	}

	closest := collections.FindSimilar(attempted, available, 1)[0]
	shown := available
	more := 0
	if len(shown) > modelRecommendationLimit {
		more = len(shown) - modelRecommendationLimit
		shown = shown[:modelRecommendationLimit]
	}
	list := strings.Join(shown, ", ")
	if more > 0 {
		list += fmt.Sprintf(" (+%d more)", more)
	}
	return fmt.Errorf("%w; did you mean %q? available models for %s: %s", err, closest, RuntimeOf(p, mode), list)
}

type modelErrorProvider struct {
	provider Provider

	modelsOnce sync.Once
	models     []ModelDef
}

func (p *modelErrorProvider) GetModel() string    { return p.provider.GetModel() }
func (p *modelErrorProvider) GetRuntime() Runtime { return p.provider.GetRuntime() }
func (p *modelErrorProvider) Unwrap() Provider    { return p.provider }

func (p *modelErrorProvider) recommend(ctx context.Context, err error) error {
	if !IsModelUnavailable(err) || strings.Contains(strings.ToLower(err.Error()), "available models for") {
		return err
	}
	runtime := p.GetRuntime()
	provider, _ := runtime.ModelProvider()
	p.modelsOnce.Do(func() {
		p.models = modelAvailabilityResolver(ctx, provider, runtime.Mode)
	})
	return recommendModelErrorFromModels(provider, runtime.Mode, p.GetModel(), err, p.models)
}

func (p *modelErrorProvider) Execute(ctx context.Context, req Request) (*Response, error) {
	resp, err := p.provider.Execute(ctx, req)
	if err != nil {
		err = p.recommend(ctx, err)
	}
	return resp, err
}

type modelErrorStreamingProvider struct {
	*modelErrorProvider
	streamer StreamingProvider
}

func (p *modelErrorStreamingProvider) ExecuteStream(ctx context.Context, req Request) (<-chan Event, error) {
	ch, err := p.streamer.ExecuteStream(ctx, req)
	if err != nil {
		return nil, p.recommend(ctx, err)
	}
	out := make(chan Event)
	go func() {
		defer close(out)
		for ev := range ch {
			if ev.Kind == EventError && ev.Error != "" {
				ev.Error = p.recommend(ctx, fmt.Errorf("%s", ev.Error)).Error()
			}
			if !sendEvent(ctx, out, ev) {
				return
			}
		}
	}()
	return out, nil
}

func withModelErrorRecommendations(provider Provider) Provider {
	base := &modelErrorProvider{provider: provider}
	if streamer, ok := provider.(StreamingProvider); ok {
		return &modelErrorStreamingProvider{modelErrorProvider: base, streamer: streamer}
	}
	return base
}
