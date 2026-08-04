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

// fetchAPIModels resolves each direct provider backend's live /v1/models
// endpoint once, concurrently. Local CLI/agent/cmux adapters deliberately do
// not participate: their model catalogs must describe the runtime they execute,
// independent of whether the parent provider's API key happens to be present.
// The resolver is Captain's cached model path, so repeated probes reuse a fresh
// cache instead of hitting providers every time; refresh bypasses that cache and
// re-queries every provider listing.
func fetchAPIModels(backends []Backend, probe AuthProbe, refresh bool) map[Backend]modelFetch {
	apis := map[Backend]bool{}
	for _, b := range backends {
		if b.Kind() != "api" {
			continue
		}
		source := modelSourceBackend(b)
		if source == "" {
			continue
		}
		if effectiveAPIKey(source, probe) != "" {
			apis[source] = true
		}
	}

	out := make(map[Backend]modelFetch, len(apis))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for b := range apis {
		wg.Add(1)
		go func(backend Backend) {
			defer wg.Done()
			rows, err := resolveModelRows(context.Background(), ResolveOptions{Backend: backend, UseTokens: true, Refresh: refresh})
			m := liveModelDefs(rows, backend)
			mu.Lock()
			out[backend] = modelFetch{models: m, err: err}
			mu.Unlock()
		}(b)
	}
	wg.Wait()
	return out
}

func fetchCodexModels(backends []Backend, probe AuthProbe) modelFetch {
	if probe.CodexModels == nil {
		return modelFetch{}
	}
	wanted := false
	for _, backend := range backends {
		if isCodexBackend(backend) {
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

func liveModelDefs(rows []ResolvedModel, backend Backend) []ModelDef {
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
			Backend:           backend,
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
// a single adapter. Direct API backends use live provider rows. Local adapters
// use the catalog of the runtime they execute: Codex's installed catalog when
// available, otherwise Captain's backend-specific registry projection.
func applyModels(st *AdapterStatus, b Backend, cache map[Backend]modelFetch, codex modelFetch, probe AuthProbe) {
	if isCodexBackend(b) {
		if codex.err == nil && len(codex.models) > 0 {
			models := make([]ModelDef, len(codex.models))
			for i, model := range codex.models {
				model.Backend = b
				models[i] = model
			}
			setModels(st, models, true)
			return
		}
		setRegistryModels(st, b, codex.err)
		return
	}
	if b.Kind() == "cli" {
		setRegistryModels(st, b, nil)
		return
	}

	source := modelSourceBackend(b)
	if source == "" {
		st.ModelError = fmt.Sprintf("backend %s has no model listing", b)
		return
	}

	envVars := AuthEnvVars(source)
	if effectiveAPIKey(source, probe) == "" {
		st.ModelError = "configure a Captain vault token or set " + strings.Join(envVars, " or ") + " to list models"
		return
	}

	fetch, ok := cache[source]
	if !ok {
		return
	}
	if fetch.err != nil {
		st.ModelError = fetch.err.Error()
		return
	}
	setModels(st, modelsForAdapterBackend(b, fetch.models), false)
}

func effectiveAPIKey(backend Backend, probe AuthProbe) string {
	if probe.APICredentials != nil {
		return probe.APICredentials[backend].Token
	}
	return firstEnv(AuthEnvVars(backend), probe.Getenv)
}

func setRegistryModels(st *AdapterStatus, backend Backend, discoveryErr error) {
	setModels(st, RegistryModelDefs(backend), true)
	if len(st.Models) > 0 {
		return
	}
	if discoveryErr != nil {
		st.ModelError = fmt.Sprintf("runtime model discovery failed: %v; registry has no models for %s", discoveryErr, backend)
		return
	}
	st.ModelError = fmt.Sprintf("registry has no models for %s", backend)
}

// modelSourceBackend maps any backend onto the API backend whose model list it
// draws from: a CLI/agent/cmux adapter serves its provider family's models.
func modelSourceBackend(backend Backend) Backend {
	return backend.Provider()
}

func modelsForAdapterBackend(backend Backend, models []ModelDef) []ModelDef {
	out := make([]ModelDef, 0, len(models))
	positions := map[string]int{}
	for _, model := range models {
		if model.Backend == BackendOpenAI {
			if known, available := RegistryModelAvailability(backend, bareProviderModelID(model.ID)); known && !available {
				continue
			}
			if IsIgnoredOpenAIModelID(model.ID) {
				if _, ok := RegistryModelDef(backend, bareProviderModelID(model.ID)); !ok {
					continue
				}
			}
		}
		id := modelIDForAdapterBackend(backend, model.ID)
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
			Backend:           backend,
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

func modelIDForAdapterBackend(backend Backend, id string) string {
	return NormalizeModelForBackend(backend, bareProviderModelID(id))
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
