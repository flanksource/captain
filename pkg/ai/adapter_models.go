package ai

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/flanksource/captain/pkg/api"
)

type modelFetch struct {
	models []ModelDef
	err    error
}

// resolveModelRows is the live model resolver, a package var so tests can
// substitute deterministic rows without hitting a provider API.
var resolveModelRows = ResolveModels

// fetchAPIModels resolves each API-mode provider's live /v1/models endpoint
// once, concurrently. Local cli/agent/cmux transports deliberately do not
// participate: their model catalogs must describe the runtime they execute,
// independent of whether the parent provider's API key happens to be present.
// The resolver is Captain's cached model path, so repeated probes reuse a fresh
// cache instead of hitting providers every time; refresh bypasses that cache and
// re-queries every provider listing.
//
// The result is keyed by provider name, not by runtime: every mode of a family
// draws its listing from the same endpoint.
func fetchAPIModels(runtimes []Runtime, credentials CredentialSnapshot, apiURLs map[string]string, refresh bool) map[string]modelFetch {
	sources := map[string]*ModelProvider{}
	for _, runtime := range runtimes {
		// Only the api mode lists from the provider's endpoint. The local modes
		// list from the static catalog or the CLI's own debug output, and
		// calling out for them would spend a credential — and fail without one —
		// on a runtime that never uses it.
		if runtime.Mode != ModeAPI {
			continue
		}
		p := modelSourceProvider(runtime)
		if p == nil {
			continue
		}
		if strings.TrimSpace(credentials.APIKey(p).Token) != "" {
			sources[p.Name] = p
		}
	}

	out := make(map[string]modelFetch, len(sources))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, p := range sources {
		wg.Add(1)
		go func(p *ModelProvider) {
			defer wg.Done()
			rows, err := resolveModelRows(context.Background(), ResolveOptions{
				Provider:    p,
				Mode:        ModeAPI,
				UseTokens:   true,
				Refresh:     refresh,
				Credentials: credentials,
				APIURL:      apiURLs[p.Name],
			})
			m := liveModelDefs(rows, p, ModeAPI)
			mu.Lock()
			out[p.Name] = modelFetch{models: m, err: err}
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	return out
}

func fetchCodexModels(runtimes []Runtime, probe AuthProbe) modelFetch {
	if probe.CodexModels == nil {
		return modelFetch{}
	}
	wanted := false
	for _, runtime := range runtimes {
		if isCodexRuntime(runtime) {
			wanted = true
			break
		}
	}
	if !wanted {
		return modelFetch{}
	}
	binary, err := probe.LookPath("codex")
	if err != nil || strings.TrimSpace(binary) == "" {
		return modelFetch{err: fmt.Errorf("codex not in PATH")}
	}
	models, err := probe.CodexModels(context.Background(), binary)
	return modelFetch{models: models, err: err}
}

// isCodexRuntime reports whether a runtime is served by the installed codex
// binary, whose own catalog is a stronger source than the registry projection.
func isCodexRuntime(runtime Runtime) bool {
	return runtime.Provider == OpenAI.Name && runtime.Mode != ModeAPI
}

func liveModelDefs(rows []ResolvedModel, p *ModelProvider, mode RuntimeMode) []ModelDef {
	out := make([]ModelDef, 0, len(rows))
	for _, row := range rows {
		if !row.Live {
			continue
		}
		id := row.RuntimeID()
		if id == "" {
			continue
		}
		name := row.Label
		if name == "" {
			name = id
		}
		out = append(out, ModelDef{
			ID:                id,
			Name:              name,
			Provider:          p.Name,
			Mode:              mode,
			ReleaseDate:       row.ReleaseDate,
			CapabilitiesKnown: true,
			Reasoning:         row.Reasoning,
			Temperature:       row.Temperature,
			SupportedEfforts:  append([]api.Effort(nil), row.SupportedEfforts...),
			DefaultEffort:     row.DefaultEffort,
			Priority:          row.Priority,
		})
	}
	return out
}

// applyModels fills in the model listing (or the reason it is unavailable) for
// a single adapter. API modes use live provider rows. Local transports use the
// catalog of the runtime they execute: Codex's installed catalog when available,
// otherwise Captain's registry projection for that (provider, mode).
func applyModels(st *AdapterStatus, runtime Runtime, cache map[string]modelFetch, codex modelFetch, probe AuthProbe) {
	p := modelSourceProvider(runtime)
	if p == nil {
		st.ModelError = fmt.Sprintf("runtime %s has no model listing", runtime)
		return
	}

	if isCodexRuntime(runtime) {
		if codex.err == nil && len(codex.models) > 0 {
			models := make([]ModelDef, len(codex.models))
			for i, model := range codex.models {
				model.Provider = p.Name
				model.Mode = runtime.Mode
				models[i] = model
			}
			setModels(st, models, true)
			return
		}
		setRegistryModels(st, p, runtime.Mode, codex.err)
		return
	}
	if runtime.Mode.Kind() == "cli" {
		setRegistryModels(st, p, runtime.Mode, nil)
		return
	}

	envVars := AuthEnvVars(p, runtime.Mode)
	if strings.TrimSpace(effectiveAPIKey(p, runtime.Mode, probe)) == "" {
		st.ModelError = "configure a Captain vault token or set " + strings.Join(envVars, " or ") + " to list models"
		return
	}

	fetch, ok := cache[p.Name]
	if !ok {
		return
	}
	if fetch.err != nil {
		st.ModelError = fetch.err.Error()
		return
	}
	setModels(st, modelsForAdapterRuntime(p, runtime.Mode, fetch.models), false)
}

func effectiveAPIKey(p *ModelProvider, mode RuntimeMode, probe AuthProbe) string {
	if probe.credentials.supplied {
		return probe.credentials.APIKey(p).Token
	}
	if probe.APICredentials != nil {
		return probe.APICredentials[p.Name].Token
	}
	return firstEnv(AuthEnvVars(p, mode), probe.Getenv)
}

func setRegistryModels(st *AdapterStatus, p *ModelProvider, mode RuntimeMode, discoveryErr error) {
	setModels(st, RegistryModelDefs(p, mode), true)
	if len(st.Models) > 0 {
		return
	}
	runtime := RuntimeOf(p, mode)
	if discoveryErr != nil {
		st.ModelError = fmt.Sprintf("runtime model discovery failed: %v; registry has no models for %s", discoveryErr, runtime)
		return
	}
	st.ModelError = fmt.Sprintf("registry has no models for %s", runtime)
}

// modelSourceProvider maps any runtime onto the provider whose model list it
// draws from: a local transport serves its provider family's models.
func modelSourceProvider(runtime Runtime) *ModelProvider {
	p, _ := api.ProviderByName(runtime.Provider)
	return p
}

func modelsForAdapterRuntime(p *ModelProvider, mode RuntimeMode, models []ModelDef) []ModelDef {
	out := make([]ModelDef, 0, len(models))
	positions := map[string]int{}
	for _, model := range models {
		if p == OpenAI {
			if known, available := RegistryModelAvailability(p, mode, bareProviderModelID(model.ID)); known && !available {
				continue
			}
			if IsIgnoredOpenAIModelID(model.ID) {
				if _, ok := RegistryModelDef(p, mode, bareProviderModelID(model.ID)); !ok {
					continue
				}
			}
		}
		// A live listing is catalog data, not a user selection, so it resolves
		// through ResolveExactModel rather than the model resolver: an id the
		// registry does not know is still a real model the provider offers, and
		// it is listed under the id the provider gave it.
		id, _ := ResolveExactModel(p, mode, bareProviderModelID(model.ID))
		if id == "" {
			continue
		}
		name := model.Name
		if name == "" {
			name = id
		}
		next := ModelDef{
			ID:                id,
			Name:              name,
			Provider:          p.Name,
			Mode:              mode,
			ReleaseDate:       model.ReleaseDate,
			CapabilitiesKnown: model.CapabilitiesKnown,
			Reasoning:         model.Reasoning,
			Temperature:       model.Temperature,
			SupportedEfforts:  append([]api.Effort(nil), model.SupportedEfforts...),
			DefaultEffort:     model.DefaultEffort,
			Priority:          model.Priority,
		}
		if idx, ok := positions[id]; ok {
			if modelDefNewer(next, out[idx]) {
				out[idx] = next
			}
			continue
		}
		positions[id] = len(out)
		out = append(out, next)
	}
	return out
}

func modelDefNewer(left, right ModelDef) bool {
	if left.Priority != right.Priority && (left.Priority > 0 || right.Priority > 0) {
		if left.Priority == 0 {
			return false
		}
		if right.Priority == 0 {
			return true
		}
		return left.Priority < right.Priority
	}
	if left.ReleaseDate == "" {
		return false
	}
	if right.ReleaseDate == "" {
		return true
	}
	return left.ReleaseDate > right.ReleaseDate
}

func bareProviderModelID(id string) string {
	id = strings.TrimSpace(id)
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return id
}

// setModels filters legacy entries and copies the sorted model list onto the
// adapter status as a count plus id list. The richer details are retained only
// for pretty output; JSON stays as the historical []string model list.
func setModels(st *AdapterStatus, models []ModelDef, curated bool) {
	if curated {
		models = CurrentCuratedModelsByReleaseDate(models)
	} else {
		models = CurrentModelsByReleaseDate(models)
	}
	st.ModelCount = len(models)
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	st.Models = ids
	st.ModelDetails = models
}
