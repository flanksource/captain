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
var modelAvailabilityResolver = availableModelsForBackend

// availableModelsForBackend returns models executable by the selected backend.
// Codex's installed catalog is the strongest source for local Codex adapters;
// direct API backends use their authenticated model endpoint. The embedded
// registry keeps recommendations useful when discovery is unavailable.
func availableModelsForBackend(ctx context.Context, backend Backend) []ModelDef {
	discoveryCtx, cancel := context.WithTimeout(ctx, modelRecommendationTimeout)
	defer cancel()
	if isCodexBackend(backend) {
		if binary, err := exec.LookPath("codex"); err == nil {
			if models, err := FetchCodexDebugModels(discoveryCtx, binary); err == nil && len(models) > 0 {
				for i := range models {
					models[i].Backend = backend
				}
				return CurrentCuratedModelsByReleaseDate(models)
			}
		}
	}
	if backend.Kind() == "api" && GetAPIKeyFromEnv(backend) != "" {
		if models, err := ListModels(discoveryCtx, backend); err == nil && len(models) > 0 {
			return CurrentModelsByReleaseDate(models)
		}
	}
	return CurrentCuratedModelsByReleaseDate(RegistryModelDefs(backend))
}

func isCodexBackend(backend Backend) bool {
	switch backend {
	case BackendCodexCLI, BackendCodexAgent, BackendCodexCmux:
		return true
	default:
		return false
	}
}

// recommendModelError preserves err while adding a backend-scoped replacement
// and a compact available-model list. It runs only after a provider has
// confirmed that the attempted model is unavailable.
func recommendModelError(ctx context.Context, backend Backend, attempted string, err error) error {
	if !IsModelUnavailable(err) || strings.Contains(strings.ToLower(err.Error()), "available models for") {
		return err
	}
	return recommendModelErrorFromModels(backend, attempted, err, modelAvailabilityResolver(ctx, backend))
}

func recommendModelErrorFromModels(backend Backend, attempted string, err error, models []ModelDef) error {
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
	return fmt.Errorf("%w; did you mean %q? available models for %s: %s", err, closest, backend, list)
}

type modelErrorProvider struct {
	provider Provider

	modelsOnce sync.Once
	models     []ModelDef
}

func (p *modelErrorProvider) GetModel() string    { return p.provider.GetModel() }
func (p *modelErrorProvider) GetBackend() Backend { return p.provider.GetBackend() }
func (p *modelErrorProvider) Unwrap() Provider    { return p.provider }

func (p *modelErrorProvider) recommend(ctx context.Context, err error) error {
	if !IsModelUnavailable(err) || strings.Contains(strings.ToLower(err.Error()), "available models for") {
		return err
	}
	p.modelsOnce.Do(func() {
		p.models = modelAvailabilityResolver(ctx, p.GetBackend())
	})
	return recommendModelErrorFromModels(p.GetBackend(), p.GetModel(), err, p.models)
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
