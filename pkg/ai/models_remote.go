package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ModelDef is the in-memory shape used by the configure wizard and `captain
// ai models`. It carries just enough metadata to render a picker row; pricing
// and context data live in pkg/ai/pricing (sourced from OpenRouter) and are
// looked up by id at render time.
type ModelDef struct {
	ID      string
	Name    string
	Backend Backend
}

// remoteModelsTimeout caps each /v1/models call. The configure wizard is an
// interactive form; if the listing cannot complete within this budget the
// caller surfaces an error to the user instead of blocking the form.
const remoteModelsTimeout = 5 * time.Second

// modelsListResponse covers the wire shape used by OpenAI, Anthropic, and
// Google's Generative Language API (with the field aliases each provider
// returns). Decoding into this permissive shape lets a single helper handle
// all three.
type modelsListResponse struct {
	Data   []modelEntry `json:"data"`
	Models []modelEntry `json:"models"`
}

type modelEntry struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Name        string `json:"name"` // gemini surfaces "models/<id>" here
}

// FetchOpenAIModels calls https://api.openai.com/v1/models and returns the
// available model IDs as ModelDefs scoped to BackendOpenAI. apiKey is sent
// as a Bearer token. An empty apiKey returns an error without making a
// request.
func FetchOpenAIModels(ctx context.Context, apiKey string) ([]ModelDef, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY is not set")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.openai.com/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	return doModelsRequest(req, BackendOpenAI)
}

// FetchAnthropicModels calls https://api.anthropic.com/v1/models and returns
// the available model IDs as ModelDefs scoped to BackendAnthropic. apiKey is
// sent via the x-api-key header along with the required anthropic-version
// header. An empty apiKey returns an error without making a request.
func FetchAnthropicModels(ctx context.Context, apiKey string) ([]ModelDef, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY is not set")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.anthropic.com/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	return doModelsRequest(req, BackendAnthropic)
}

// FetchGeminiModels calls Google's Generative Language ListModels endpoint
// and returns the IDs scoped to BackendGemini. The endpoint authenticates via
// a `key=` query parameter (no header). The returned `name` field is shaped
// "models/gemini-2.5-flash"; we strip the prefix so callers see the bare id.
func FetchGeminiModels(ctx context.Context, apiKey string) ([]ModelDef, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY is not set")
	}
	url := "https://generativelanguage.googleapis.com/v1beta/models?key=" + apiKey
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return doModelsRequest(req, BackendGemini)
}

// doModelsRequest issues req with the default client and decodes the
// permissive listing shape, projecting each entry into a ModelDef tagged
// with the supplied backend. Centralising this keeps the three fetchers
// behaviourally identical (same timeouts, same error messages, same name
// normalisation).
func doModelsRequest(req *http.Request, backend Backend) ([]ModelDef, error) {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s models: %w", backend, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s models: HTTP %d", backend, resp.StatusCode)
	}

	var body modelsListResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("%s models decode: %w", backend, err)
	}

	entries := body.Data
	if len(entries) == 0 {
		entries = body.Models
	}

	out := make([]ModelDef, 0, len(entries))
	for _, m := range entries {
		id := m.ID
		if id == "" {
			id = strings.TrimPrefix(m.Name, "models/")
		}
		if id == "" {
			continue
		}
		name := m.DisplayName
		if name == "" {
			name = id
		}
		out = append(out, ModelDef{ID: id, Name: name, Backend: backend})
	}
	return out, nil
}

// ListModels fetches the live model catalogue for a backend. Live data is the
// only source of truth: there is no static fallback, so a missing API key or
// a network failure surfaces as an error to the caller. CLI backends inherit
// from their parent provider's API (claude-cli ↔ Anthropic, codex-cli ↔
// OpenAI, gemini-cli ↔ Gemini) because the CLIs themselves don't expose a
// listing endpoint and run against those same models under the hood.
func ListModels(ctx context.Context, backend Backend) ([]ModelDef, error) {
	fetch, apiKey, parent := remoteFetcherFor(backend)
	if fetch == nil {
		return nil, fmt.Errorf("backend %s has no live model listing", backend)
	}

	fetchCtx, cancel := context.WithTimeout(ctx, remoteModelsTimeout)
	defer cancel()

	models, err := fetch(fetchCtx, apiKey)
	if err != nil {
		return nil, err
	}

	// CLI backends share the underlying API but want their own backend tag
	// on display so the picker stays grouped by chosen UX (claude-cli rows
	// are visually distinct from anthropic API rows even though the IDs
	// overlap).
	if parent != backend {
		for i := range models {
			models[i].Backend = backend
		}
	}

	sort.SliceStable(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

// remoteFetcherFor returns the live-list function, API key, and parent API
// backend for the given backend. CLI backends are routed to the API of the
// same provider — they call the same models under the hood. Returns
// (nil, "", "") for backends without a known listing path.
func remoteFetcherFor(backend Backend) (fetch func(context.Context, string) ([]ModelDef, error), apiKey string, parent Backend) {
	switch backend {
	case BackendOpenAI, BackendCodexCLI:
		return FetchOpenAIModels, GetAPIKeyFromEnv(backend), BackendOpenAI
	case BackendAnthropic, BackendClaudeCLI, BackendClaudeAgent:
		return FetchAnthropicModels, GetAPIKeyFromEnv(backend), BackendAnthropic
	case BackendGemini, BackendGeminiCLI:
		return FetchGeminiModels, GetAPIKeyFromEnv(backend), BackendGemini
	default:
		return nil, "", ""
	}
}
