package claudeagent

import (
	"context"
	"fmt"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/callertools"
	aitools "github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"
)

type callerToolServer struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

func (p *Provider) prepareCallerTools(req ai.Request) error {
	p.callerToolsMu.Lock()
	defer p.callerToolsMu.Unlock()
	if p.callerTools != nil {
		return p.callerTools.Validate()
	}
	if len(p.cfg.Tools) == 0 {
		return nil
	}
	definitions, err := aitools.ResolveDefinitions(p.cfg.Tools, aitools.ResolveOptions{Preferences: req.ToolPreferences, Policy: req.ToolPolicy})
	if err != nil {
		return fmt.Errorf("claude-agent caller tools: %w", err)
	}
	if len(definitions) == 0 {
		return nil
	}
	options, err := p.callerToolOptions(req, definitions)
	if err != nil {
		return err
	}
	runtime, err := callertools.New(options)
	if err != nil {
		return fmt.Errorf("start claude-agent caller tools: %w", err)
	}
	endpoint := runtime.Endpoint()
	p.callerToolsRuntime = runtime
	p.callerTools = &endpoint
	return nil
}

// callerToolOptions scopes each caller-tool approval to the turn that made the
// call, so a pending approval ends with its turn, and bounds it by the run's
// approvalTimeout rather than the runtime's default.
func (p *Provider) callerToolOptions(req ai.Request, definitions []api.ToolDefinition) (callertools.Options, error) {
	approvalTimeout, err := req.Permissions.ParseApprovalTimeout()
	if err != nil {
		return callertools.Options{}, fmt.Errorf("claude-agent caller tools: %w", err)
	}
	return callertools.Options{
		// Owned by the provider, which outlives any one request.
		Context: context.Background(),
		ContextForCall: func() context.Context {
			if turn := p.activeTurn(); turn != nil {
				return turn.ctx
			}
			return nil
		},
		Definitions:     definitions,
		OnApproval:      p.cfg.OnApproval,
		SessionID:       firstNonEmpty(p.cfg.CaptainSessionID, req.SessionID, p.cfg.SessionID),
		ApprovalTimeout: approvalTimeout,
	}, nil
}

func callerToolServers(endpoint *api.CallerToolEndpoint) map[string]callerToolServer {
	if endpoint == nil {
		return nil
	}
	return map[string]callerToolServer{
		endpoint.Name: {Type: "http", URL: endpoint.URL, Headers: cloneHeaders(endpoint.Headers)},
	}
}

func callerToolUseIDKey(endpoint *api.CallerToolEndpoint) string {
	if endpoint == nil {
		return ""
	}
	return callertools.ToolUseIDInputKey
}

func cloneHeaders(headers map[string]string) map[string]string {
	if headers == nil {
		return nil
	}
	cloned := make(map[string]string, len(headers))
	for key, value := range headers {
		cloned[key] = value
	}
	return cloned
}
