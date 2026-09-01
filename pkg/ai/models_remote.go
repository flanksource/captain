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
	ID   string `json:"id"`
	Name string `json:"label,omitempty"`
	// Provider names the family that owns the model id; Mode names the mechanism
	// it runs on. Together they are the runtime — there is no composite id.
	Provider          string          `json:"provider,omitempty"`
	Mode              api.RuntimeMode `json:"mode,omitempty"`
	ReleaseDate       string          `json:"releaseDate,omitempty"`
	CapabilitiesKnown bool            `json:"capabilitiesKnown,omitempty"`
	Reasoning         bool            `json:"reasoning,omitempty"`
	Temperature       bool            `json:"temperature"`
	InputMediaTypes   []string        `json:"inputMediaTypes,omitempty"`
	SupportedEfforts  []api.Effort    `json:"supportedEfforts,omitempty"`
	DefaultEffort     api.Effort      `json:"defaultEffort,omitempty"`
	Priority          int             `json:"priority,omitempty"`

	// Disabled marks a model the user has taken out of circulation. It is set by
	// ApplyDisabled on the whoami probe only — the page must still render the row
	// so the toggle has something to switch back on. Every other consumer drops
	// disabled models rather than annotating them.
	Disabled bool `json:"disabled,omitempty"`
}

// ModelProvider resolves the provider descriptor behind Provider, or nil when
// the row carries no provider (a raw listing that was never attributed).
func (m ModelDef) ModelProvider() *api.ModelProvider {
	p, _ := api.ProviderByName(m.Provider)
	return p
}

// remoteModelsTimeout caps each /v1/models call. The configure wizard is an
// interactive form; if the listing cannot complete within this budget the
// caller surfaces an error to the user instead of blocking the form.
const remoteModelsTimeout = 5 * time.Second

var defaultModelListEndpoints = map[string]string{
	Anthropic.Name: "https://api.anthropic.com/v1/models",
	OpenAI.Name:    "https://api.openai.com/v1/models",
	Google.Name:    "https://generativelanguage.googleapis.com/v1beta/models",
	DeepSeek.Name:  "https://api.deepseek.com/models",
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
	Provider   string
	StatusCode int
}

func (e ModelHTTPError) Error() string {
	return fmt.Sprintf("%s models: HTTP %d", e.Provider, e.StatusCode)
}

// ModelTransportError keeps custom endpoint details out of user-visible errors
// while preserving the underlying cause for errors.Is/errors.As callers.
type ModelTransportError struct {
	Provider string
	Err      error
}

func (e ModelTransportError) Error() string {
	return fmt.Sprintf("%s models: transport request failed", e.Provider)
}

func (e ModelTransportError) Unwrap() error { return e.Err }

// FetchOpenAIModels calls https://api.openai.com/v1/models and returns the
// available model IDs as ModelDefs scoped to BackendOpenAI. apiKey is sent
// as a Bearer token. An empty apiKey returns an error without making a
// request.
func FetchOpenAIModels(ctx context.Context, apiKey string) ([]ModelDef, error) {
	return fetchModelsAtEndpoint(ctx, OpenAI, apiKey, defaultModelListEndpoints[OpenAI.Name])
}

// FetchAnthropicModels calls https://api.anthropic.com/v1/models and returns
// the available model IDs as ModelDefs scoped to BackendAnthropic. apiKey is
// sent via the x-api-key header along with the required anthropic-version
// header. An empty apiKey returns an error without making a request.
func FetchAnthropicModels(ctx context.Context, apiKey string) ([]ModelDef, error) {
	return fetchModelsAtEndpoint(ctx, Anthropic, apiKey, defaultModelListEndpoints[Anthropic.Name])
}

// FetchGeminiModels calls Google's Generative Language ListModels endpoint
// and returns the IDs scoped to BackendGemini. The endpoint authenticates via
// the x-goog-api-key header. The returned `name` field is shaped
// "models/gemini-2.5-flash"; we strip the prefix so callers see the bare id.
func FetchGeminiModels(ctx context.Context, apiKey string) ([]ModelDef, error) {
	return fetchModelsAtEndpoint(ctx, Google, apiKey, defaultModelListEndpoints[Google.Name])
}

// FetchDeepSeekModels calls https://api.deepseek.com/models and returns the
// available model IDs as ModelDefs scoped to BackendDeepSeek. DeepSeek's API is
// OpenAI-compatible, so the endpoint is a Bearer-authenticated, OpenAI-shaped
// listing. An empty apiKey returns an error without making a request.
func FetchDeepSeekModels(ctx context.Context, apiKey string) ([]ModelDef, error) {
	return fetchModelsAtEndpoint(ctx, DeepSeek, apiKey, defaultModelListEndpoints[DeepSeek.Name])
}

// modelListEndpoint resolves a provider base URL to the exact URL used by the
// model-list request. APIURL follows Config.APIURL's base-URL convention; a URL
// already ending in /models is also accepted. Gemini deliberately retains its
// existing no-custom-endpoint contract.
func modelListEndpoint(p *ModelProvider, apiURL string) (string, error) {
	apiURL = strings.TrimSpace(apiURL)
	if apiURL == "" {
		endpoint, ok := defaultModelListEndpoints[p.Name]
		if !ok {
			return "", fmt.Errorf("provider %s has no live model listing", p.Name)
		}
		return endpoint, nil
	}
	if p == Google {
		return "", fmt.Errorf("provider %s does not support a custom model-list endpoint", p.Name)
	}
	u, err := url.Parse(apiURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("invalid model-list endpoint for %s", p.Name)
	}
	u.Fragment = ""
	path := strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(path, "/models") {
		switch p {
		case Anthropic:
			if strings.HasSuffix(path, "/v1") {
				path += "/models"
			} else {
				path += "/v1/models"
			}
		case OpenAI, DeepSeek:
			path += "/models"
		default:
			return "", fmt.Errorf("provider %s has no live model listing", p.Name)
		}
	}
	u.Path = path
	return u.String(), nil
}

func modelAPIURLEnvVars(p *ModelProvider) []string {
	switch p {
	case Anthropic:
		return []string{"ANTHROPIC_BASE_URL"}
	case OpenAI:
		return []string{"OPENAI_BASE_URL"}
	case DeepSeek:
		return []string{"DEEPSEEK_BASE_URL"}
	default:
		return nil
	}
}

func fetchModelsAtEndpoint(ctx context.Context, p *ModelProvider, apiKey, endpoint string) ([]ModelDef, error) {
	if strings.TrimSpace(apiKey) == "" {
		envVars := AuthEnvVars(p, ModeAPI)
		if len(envVars) == 0 {
			return nil, fmt.Errorf("provider %s has no live model listing", p.Name)
		}
		return nil, fmt.Errorf("%s is not set", envVars[0])
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build %s models request: %w", p.Name, err)
	}
	switch p {
	case Anthropic:
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	case Google:
		req.Header.Set("x-goog-api-key", apiKey)
	case OpenAI, DeepSeek:
		req.Header.Set("Authorization", "Bearer "+apiKey)
	default:
		return nil, fmt.Errorf("provider %s has no live model listing", p.Name)
	}
	return doModelsRequest(req, p)
}

// doModelsRequest issues req with the default client and decodes the
// permissive listing shape, projecting each entry into a ModelDef tagged
// with the supplied backend. Centralising this keeps the provider fetchers
// behaviourally identical (same timeouts, same error messages, same name
// normalisation).
func doModelsRequest(req *http.Request, p *ModelProvider) ([]ModelDef, error) {
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
		return nil, ModelTransportError{Provider: p.Name, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, ModelHTTPError{Provider: p.Name, StatusCode: resp.StatusCode}
	}

	var body modelsListResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("%s models decode: %w", p.Name, err)
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
			releaseDate = CatalogReleaseDate(p, ModeAPI, id)
		}
		// A live listing is the api mode's catalogue by construction — the local
		// modes have no remote endpoint — so the row names its whole runtime
		// rather than leaving the mode for a caller to fill in.
		def := ModelDef{ID: id, Name: name, Provider: p.Name, Mode: ModeAPI, ReleaseDate: releaseDate}
		if registry, ok := RegistryModelDef(p, ModeAPI, id); ok {
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

// ListModels fetches the live model catalogue for a provider's API mode. Live
// data is the only source of truth: there is no static fallback, so a missing
// API key or a network failure surfaces as an error to the caller. The local
// transports have no live listing here — they authenticate internally and
// enumerate their models from the static catalog (AgentCatalogModels).
func ListModels(ctx context.Context, p *ModelProvider) ([]ModelDef, error) {
	resolved, err := ResolveAPIKey(p, ModeAPI)
	if err != nil {
		return nil, err
	}
	return ListModelsWithAPIKey(ctx, p, resolved.Token)
}

// ListModelsWithAPIKey validates a candidate credential directly against the
// provider model endpoint without reading or writing Captain's credential vault.
func ListModelsWithAPIKey(ctx context.Context, p *ModelProvider, apiKey string) ([]ModelDef, error) {
	return ListModelsWithAPIKeyAndURL(ctx, p, apiKey, "")
}

// ListModelsWithAPIKeyAndURL is ListModelsWithAPIKey with an optional provider
// base-URL override. The exact resolved request URL is shared with the persisted
// model-cache identity so endpoint-specific availability cannot cross caches.
func ListModelsWithAPIKeyAndURL(ctx context.Context, p *ModelProvider, apiKey, apiURL string) ([]ModelDef, error) {
	endpoint, err := modelListEndpoint(p, apiURL)
	if err != nil {
		return nil, err
	}
	return listModelsWithAPIKeyAtEndpoint(ctx, p, apiKey, endpoint)
}

func listModelsWithAPIKeyAtEndpoint(ctx context.Context, p *ModelProvider, apiKey, endpoint string) ([]ModelDef, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, remoteModelsTimeout)
	defer cancel()

	models, err := fetchModelsAtEndpoint(fetchCtx, p, apiKey, endpoint)
	if err != nil {
		return nil, err
	}

	sort.SliceStable(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}
