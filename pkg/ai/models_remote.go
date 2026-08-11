package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api"
)

// ModelDef is the in-memory shape used by the configure wizard and `captain
// ai models`. It carries just enough metadata to render a picker row; pricing
// and context data live in pkg/ai/pricing (sourced from OpenRouter) and are
// looked up by id at render time.
type ModelDef struct {
	ID                string       `json:"id"`
	Name              string       `json:"label,omitempty"`
	Backend           Backend      `json:"backend,omitempty"`
	ReleaseDate       string       `json:"releaseDate,omitempty"`
	CapabilitiesKnown bool         `json:"capabilitiesKnown,omitempty"`
	Reasoning         bool         `json:"reasoning,omitempty"`
	Temperature       bool         `json:"temperature"`
	InputMediaTypes   []string     `json:"inputMediaTypes,omitempty"`
	SupportedEfforts  []api.Effort `json:"supportedEfforts,omitempty"`
	DefaultEffort     api.Effort   `json:"defaultEffort,omitempty"`
	Priority          int          `json:"priority,omitempty"`

	// Disabled marks a model the user has taken out of circulation. It is set by
	// ApplyDisabled on the whoami probe only — the page must still render the row
	// so the toggle has something to switch back on. Every other consumer drops
	// disabled models rather than annotating them.
	Disabled bool `json:"disabled,omitempty"`
}

// remoteModelsTimeout caps each /v1/models call. The configure wizard is an
// interactive form; if the listing cannot complete within this budget the
// caller surfaces an error to the user instead of blocking the form.
const remoteModelsTimeout = 5 * time.Second

var defaultModelListEndpoints = map[Backend]string{
	BackendAnthropic: "https://api.anthropic.com/v1/models",
	BackendOpenAI:    "https://api.openai.com/v1/models",
	BackendGemini:    "https://generativelanguage.googleapis.com/v1beta/models",
	BackendDeepSeek:  "https://api.deepseek.com/models",
}

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
	Created     int64  `json:"created"`
	CreatedAt   string `json:"created_at"`
}

type ModelHTTPError struct {
	Backend    Backend
	StatusCode int
}

func (e ModelHTTPError) Error() string {
	return fmt.Sprintf("%s models: HTTP %d", e.Backend, e.StatusCode)
}

// ModelTransportError keeps custom endpoint details out of user-visible errors
// while preserving the underlying cause for errors.Is/errors.As callers.
type ModelTransportError struct {
	Backend Backend
	Err     error
}

func (e ModelTransportError) Error() string {
	return fmt.Sprintf("%s models: transport request failed", e.Backend)
}

func (e ModelTransportError) Unwrap() error { return e.Err }

// FetchOpenAIModels calls https://api.openai.com/v1/models and returns the
// available model IDs as ModelDefs scoped to BackendOpenAI. apiKey is sent
// as a Bearer token. An empty apiKey returns an error without making a
// request.
func FetchOpenAIModels(ctx context.Context, apiKey string) ([]ModelDef, error) {
	return fetchModelsAtEndpoint(ctx, BackendOpenAI, apiKey, defaultModelListEndpoints[BackendOpenAI])
}

// FetchAnthropicModels calls https://api.anthropic.com/v1/models and returns
// the available model IDs as ModelDefs scoped to BackendAnthropic. apiKey is
// sent via the x-api-key header along with the required anthropic-version
// header. An empty apiKey returns an error without making a request.
func FetchAnthropicModels(ctx context.Context, apiKey string) ([]ModelDef, error) {
	return fetchModelsAtEndpoint(ctx, BackendAnthropic, apiKey, defaultModelListEndpoints[BackendAnthropic])
}

// FetchGeminiModels calls Google's Generative Language ListModels endpoint
// and returns the IDs scoped to BackendGemini. The endpoint authenticates via
// the x-goog-api-key header. The returned `name` field is shaped
// "models/gemini-2.5-flash"; we strip the prefix so callers see the bare id.
func FetchGeminiModels(ctx context.Context, apiKey string) ([]ModelDef, error) {
	return fetchModelsAtEndpoint(ctx, BackendGemini, apiKey, defaultModelListEndpoints[BackendGemini])
}

// FetchDeepSeekModels calls https://api.deepseek.com/models and returns the
// available model IDs as ModelDefs scoped to BackendDeepSeek. DeepSeek's API is
// OpenAI-compatible, so the endpoint is a Bearer-authenticated, OpenAI-shaped
// listing. An empty apiKey returns an error without making a request.
func FetchDeepSeekModels(ctx context.Context, apiKey string) ([]ModelDef, error) {
	return fetchModelsAtEndpoint(ctx, BackendDeepSeek, apiKey, defaultModelListEndpoints[BackendDeepSeek])
}

// modelListEndpoint resolves a provider base URL to the exact URL used by the
// model-list request. APIURL follows Config.APIURL's base-URL convention; a URL
// already ending in /models is also accepted. Gemini deliberately retains its
// existing no-custom-endpoint contract.
func modelListEndpoint(backend Backend, apiURL string) (string, error) {
	apiURL = strings.TrimSpace(apiURL)
	if apiURL == "" {
		endpoint, ok := defaultModelListEndpoints[backend]
		if !ok {
			return "", fmt.Errorf("backend %s has no live model listing", backend)
		}
		return endpoint, nil
	}
	if backend == BackendGemini {
		return "", fmt.Errorf("backend %s does not support a custom model-list endpoint", backend)
	}
	u, err := url.Parse(apiURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("invalid model-list endpoint for %s", backend)
	}
	u.Fragment = ""
	path := strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(path, "/models") {
		switch backend {
		case BackendAnthropic:
			if strings.HasSuffix(path, "/v1") {
				path += "/models"
			} else {
				path += "/v1/models"
			}
		case BackendOpenAI, BackendDeepSeek:
			path += "/models"
		default:
			return "", fmt.Errorf("backend %s has no live model listing", backend)
		}
	}
	u.Path = path
	return u.String(), nil
}

func modelAPIURLEnvVars(backend Backend) []string {
	switch backend {
	case BackendAnthropic:
		return []string{"ANTHROPIC_BASE_URL"}
	case BackendOpenAI:
		return []string{"OPENAI_BASE_URL"}
	case BackendDeepSeek:
		return []string{"DEEPSEEK_BASE_URL"}
	default:
		return nil
	}
}

func fetchModelsAtEndpoint(ctx context.Context, backend Backend, apiKey, endpoint string) ([]ModelDef, error) {
	if strings.TrimSpace(apiKey) == "" {
		envVars := AuthEnvVars(backend)
		if len(envVars) == 0 {
			return nil, fmt.Errorf("backend %s has no live model listing", backend)
		}
		return nil, fmt.Errorf("%s is not set", envVars[0])
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build %s models request: %w", backend, err)
	}
	switch backend {
	case BackendAnthropic:
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	case BackendGemini:
		req.Header.Set("x-goog-api-key", apiKey)
	case BackendOpenAI, BackendDeepSeek:
		req.Header.Set("Authorization", "Bearer "+apiKey)
	default:
		return nil, fmt.Errorf("backend %s has no live model listing", backend)
	}
	return doModelsRequest(req, backend)
}

// doModelsRequest issues req with the default client and decodes the
// permissive listing shape, projecting each entry into a ModelDef tagged
// with the supplied backend. Centralising this keeps the provider fetchers
// behaviourally identical (same timeouts, same error messages, same name
// normalisation).
func doModelsRequest(req *http.Request, backend Backend) ([]ModelDef, error) {
	client := *http.DefaultClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := client.Do(req)
	if err != nil {
		// url.Error includes the full request URL, which may carry tenant or
		// credential-bearing query data on custom endpoints. Preserve the cause
		// for programmatic inspection but never render it to the user.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, ModelTransportError{Backend: backend, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, ModelHTTPError{Backend: backend, StatusCode: resp.StatusCode}
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
		releaseDate := m.releaseDate()
		if releaseDate == "" {
			releaseDate = CatalogReleaseDate(backend, id)
		}
		def := ModelDef{ID: id, Name: name, Backend: backend, ReleaseDate: releaseDate}
		if registry, ok := RegistryModelDef(backend, id); ok {
			def.CapabilitiesKnown = registry.CapabilitiesKnown
			def.Reasoning = registry.Reasoning
			def.Temperature = registry.Temperature
			def.SupportedEfforts = registry.SupportedEfforts
			def.DefaultEffort = registry.DefaultEffort
			def.Priority = registry.Priority
		}
		out = append(out, def)
	}
	return out, nil
}

func (m modelEntry) releaseDate() string {
	if m.Created > 0 {
		return time.Unix(m.Created, 0).UTC().Format("2006-01-02")
	}
	return normalizeReleaseDate(m.CreatedAt)
}

// ListModels fetches the live model catalogue for an API backend. Live data is
// the only source of truth: there is no static fallback, so a missing API key
// or a network failure surfaces as an error to the caller. CLI/agent backends
// have no live listing here — they authenticate internally and enumerate their
// models from the static catalog in pkg/cli (agentCatalogModels), so passing
// one returns an error.
func ListModels(ctx context.Context, backend Backend) ([]ModelDef, error) {
	resolved, err := ResolveAPIKey(backend)
	if err != nil {
		return nil, err
	}
	return ListModelsWithAPIKey(ctx, backend, resolved.Token)
}

// ListModelsWithAPIKey validates a candidate credential directly against the
// provider model endpoint without reading or writing Captain's credential vault.
func ListModelsWithAPIKey(ctx context.Context, backend Backend, apiKey string) ([]ModelDef, error) {
	return ListModelsWithAPIKeyAndURL(ctx, backend, apiKey, "")
}

// ListModelsWithAPIKeyAndURL is ListModelsWithAPIKey with an optional provider
// base-URL override. The exact resolved request URL is shared with the persisted
// model-cache identity so endpoint-specific availability cannot cross caches.
func ListModelsWithAPIKeyAndURL(ctx context.Context, backend Backend, apiKey, apiURL string) ([]ModelDef, error) {
	endpoint, err := modelListEndpoint(backend, apiURL)
	if err != nil {
		return nil, err
	}
	return listModelsWithAPIKeyAtEndpoint(ctx, backend, apiKey, endpoint)
}

func listModelsWithAPIKeyAtEndpoint(ctx context.Context, backend Backend, apiKey, endpoint string) ([]ModelDef, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, remoteModelsTimeout)
	defer cancel()

	models, err := fetchModelsAtEndpoint(fetchCtx, backend, apiKey, endpoint)
	if err != nil {
		return nil, err
	}

	sort.SliceStable(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}
