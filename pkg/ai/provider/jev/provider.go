package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	native "github.com/flanksource/captain/pkg/ai/jev"
	"github.com/flanksource/captain/pkg/api"
)

type Provider struct {
	model    string
	apiKey   string
	endpoint string
	client   *http.Client
}

func New(cfg ai.Config) (*Provider, error) {
	if strings.TrimSpace(cfg.Model.Name) == "" {
		return nil, fmt.Errorf("jev requires a model name")
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("%w: Jev requires TYPESAFE_API_KEY", ai.ErrNoAPIKey)
	}
	if cfg.Model.Temperature != nil || cfg.Model.Effort != "" {
		return nil, fmt.Errorf("jev does not support temperature or reasoning effort")
	}
	if cfg.SessionID != "" {
		return nil, fmt.Errorf("jev does not support session resume")
	}
	endpoint, err := systemOneEndpoint(cfg.APIURL)
	if err != nil {
		return nil, err
	}
	client := *http.DefaultClient
	client.Timeout = 30 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Provider{model: cfg.Model.Name, apiKey: cfg.APIKey, endpoint: endpoint, client: &client}, nil
}

func systemOneEndpoint(base string) (string, error) {
	if base == "" {
		base = "https://api.typesafe.ai"
	}
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("jev API URL must be an absolute HTTP or HTTPS base URL without credentials or query parameters")
	}
	if strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/v1") {
		u.Path = strings.TrimRight(u.Path, "/") + "/systemone"
	} else {
		u.Path = strings.TrimRight(u.Path, "/") + "/v1/systemone"
	}
	return u.String(), nil
}

func (p *Provider) GetModel() string { return p.model }

func (p *Provider) GetRuntime() ai.Runtime { return ai.RuntimeOf(ai.Jev, ai.ModeAPI) }

func (p *Provider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	request, err := p.prepare(req)
	if err != nil {
		return nil, err
	}
	result, err := p.send(ctx, request)
	if err != nil {
		return nil, err
	}
	return p.response(result), nil
}

func (p *Provider) ExecuteStream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	request, err := p.prepare(req)
	if err != nil {
		return nil, err
	}
	out := make(chan ai.Event, 5)
	go func() {
		defer close(out)
		out <- ai.Event{Kind: ai.EventTurn, Message: &api.Message{Role: api.RoleUser, Parts: []api.Part{{Type: api.PartText, Text: req.Prompt.User}}}}
		result, err := p.send(ctx, request)
		if err != nil {
			out <- ai.Event{Kind: ai.EventError, Error: err.Error(), Raw: err, Model: p.model}
			return
		}
		response := p.response(result)
		out <- ai.Event{Kind: ai.EventTurn, Message: &api.Message{Role: api.RoleAssistant, Parts: []api.Part{{Type: api.PartText, Text: response.Text}}}, Usage: &response.Usage, Model: response.Model}
		out <- ai.Event{Kind: ai.EventText, Text: response.Text, Model: response.Model}
		out <- ai.Event{Kind: ai.EventResult, Success: true, Model: response.Model, Usage: &response.Usage, CostUSD: response.CostUSD, StructuredData: result.raw, Raw: result.decoded}
	}()
	return out, nil
}

func (p *Provider) prepare(req ai.Request) (native.Request, error) {
	if strings.TrimSpace(req.Prompt.User) == "" {
		return native.Request{}, fmt.Errorf("jev requires prompt.user containing a native TypeSafe request")
	}
	if len(req.Messages) > 0 || req.SessionID != "" {
		return native.Request{}, fmt.Errorf("jev does not support conversation history or session resume")
	}
	if len(req.Prompt.Attachments) > 0 {
		return native.Request{}, fmt.Errorf("jev does not support attachments")
	}
	if req.Temperature != nil || req.Effort != "" {
		return native.Request{}, fmt.Errorf("jev does not support temperature or reasoning effort")
	}
	if req.Prompt.HasSchema() || len(req.Prompt.TypeSafeQuestions) > 0 || req.Prompt.System != "" || req.Prompt.AppendSystem != "" {
		return native.Request{}, fmt.Errorf("jev native provider requires middleware to convert schema and system instructions")
	}
	request, err := native.ParseRequest([]byte(req.Prompt.User))
	if err != nil {
		return native.Request{}, err
	}
	if request.Model != p.model {
		return native.Request{}, fmt.Errorf("jev native request model %q differs from selected model %q", request.Model, p.model)
	}
	return request, nil
}

func (p *Provider) send(ctx context.Context, request native.Request) (*jevResult, error) {
	start := time.Now()
	body, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("jev encode request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("jev build request: %w", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := p.client.Do(httpRequest)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, fmt.Errorf("jev request failed: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jev API returned HTTP %d", response.StatusCode)
	}
	result, err := decodeResponse(response.Body, request)
	if err != nil {
		return nil, err
	}
	result.duration = time.Since(start)
	return result, nil
}

func (p *Provider) response(result *jevResult) *ai.Response {
	return &ai.Response{
		Text: string(result.raw), StructuredData: result.raw, Model: result.decoded.Model,
		Runtime: p.GetRuntime(), Usage: result.usage, Raw: result.decoded, Duration: result.duration,
		CostUSD: ai.PriceUsage(ai.Jev, p.model, result.usage, 0).Total(),
	}
}
