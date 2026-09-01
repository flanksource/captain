package api

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// PermissionFunc decides whether an agent may run a tool. It is invoked by a
// streaming provider on a can_use_tool control request; returning an error denies
// the tool with the error text fed back to the agent as the reason.
type PermissionFunc func(ctx context.Context, req PermissionRequest) (PermissionDecision, error)

// PermissionRequest describes the tool an agent wants to run. SessionID is filled
// in by the provider from the live session so a caller can key approvals by it.
type PermissionRequest struct {
	Tool               string
	Input              map[string]any
	ToolUseID          string
	ToolUseIDGenerated bool
	SessionID          string
}

// PermissionDecision is the answer to a PermissionRequest. On Allow the tool runs
// (with UpdatedInput substituted when non-nil); otherwise it is denied and Message
// is fed back to the agent as the reason.
type PermissionDecision struct {
	Allow        bool
	Message      string
	UpdatedInput map[string]any
}

// SchemaRepairConfig controls the optional second pass used when structured
// output fails local JSON-schema validation. Empty means use the parent
// provider/model and captain's embedded repair prompt.
type SchemaRepairConfig struct {
	Model  Model  // optional override; empty means the parent model/backend
	Prompt string // optional .prompt file path; empty means embedded default
}

// CallerToolEndpoint is an authenticated, request-scoped MCP endpoint exposing
// caller-owned tools. Headers are transport credentials and must never be
// serialized into specs, command arguments, events, or logs.
type CallerToolEndpoint struct {
	Name    string
	URL     string
	Headers map[string]string
}

func (endpoint CallerToolEndpoint) Validate() error {
	if endpoint.Name == "" {
		return fmt.Errorf("caller-tool endpoint name is required")
	}
	for _, value := range endpoint.Name {
		if !isCallerToolNameRune(value) {
			return fmt.Errorf("caller-tool endpoint name %q contains unsupported characters", endpoint.Name)
		}
	}
	parsed, err := url.Parse(endpoint.URL)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("caller-tool endpoint URL must be an absolute HTTP or HTTPS URL")
	}
	if parsed.User != nil {
		return fmt.Errorf("caller-tool endpoint URL must not contain credentials")
	}
	if parsed.Scheme == "http" && !isLoopbackHost(parsed.Hostname()) {
		return fmt.Errorf("caller-tool endpoint requires HTTPS outside loopback")
	}
	authorization := ""
	for name, value := range endpoint.Headers {
		if strings.EqualFold(name, "Authorization") {
			authorization = strings.TrimSpace(value)
			break
		}
	}
	if !strings.HasPrefix(authorization, "Bearer ") || strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer ")) == "" {
		return fmt.Errorf("caller-tool endpoint requires a bearer credential")
	}
	return nil
}

func isCallerToolNameRune(value rune) bool {
	return value >= 'a' && value <= 'z' ||
		value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' ||
		value == '-' ||
		value == '_'
}

func isLoopbackHost(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}

// Config is the provider construction/runtime config. Model (name/backend/temp/
// effort) and Budget (cost ceiling, max tokens) come from the serializable spec
// types; the rest are transport/runtime concerns that never belong in Spec. It is
// part of the stable runtime contract: a consumer constructs a Config and hands it
// to NewProvider.
type Config struct {
	Model  Model  // Name, ID, Backend (empty = infer), Temperature, Effort
	Budget Budget // Cost (USD ceiling, 0 = unlimited), MaxTokens
	APIKey string // empty = env lookup
	// APIURL overrides the backend's endpoint (empty = the provider default).
	// Anthropic/OpenAI/DeepSeek honour it; Gemini rejects it, because genkit's
	// googlegenai plugin exposes no override and silently calling the real API
	// would be worse. Claude Agent passes it through to the SDK child; Codex CLI
	// and Codex Agent declare a model_providers entry because stored account auth
	// otherwise takes precedence over OPENAI_BASE_URL.
	APIURL string
	// SandboxSelection is the resolved external execution boundary for the run.
	// Off and Native use identity adapters; Docker wraps a local CLI; Git Agent
	// relocates execution. Provider-native policy stays on Spec.Sandbox.
	SandboxSelection *SandboxConfig
	CacheDBPath      string
	CacheTTL         time.Duration
	NoCache          bool
	MaxConcurrent    int
	SessionID        string
	// CaptainSessionID is Captain's own session/thread UUID, as distinct from
	// SessionID (the provider's id for the same conversation). Caller-tool MCP
	// endpoints are scoped by it so an approval brokered for one Captain thread
	// cannot be replayed against another that happens to share a provider id.
	CaptainSessionID string
	ProjectName      string
	SchemaRepair     SchemaRepairConfig

	// CanUseTool, when set, brokers tool permissions over the stream-json control
	// protocol: the streaming provider asks this callback before a tool that needs
	// approval runs, and forwards the decision to the agent. Only providers that
	// support a server→client permission round-trip honour it (the anthropic agent);
	// others ignore it. A nil callback keeps the auto-approve (bypass) behaviour.
	// It is never serialized (the agent process never sees the Go closure).
	CanUseTool PermissionFunc `json:"-"`

	// Tools are caller-supplied tools exposed to the model and executed
	// in-process. Tool-capable API providers invoke the handlers directly;
	// out-of-process agent providers expose them through a private Captain MCP
	// endpoint. Never serialized (Go closures).
	Tools []ToolDefinition `json:"-"`

	// CallerTools supplies a pre-issued Captain MCP endpoint. When nil, an
	// out-of-process tool-capable provider creates a private loopback endpoint
	// from Tools. It is runtime-only because Headers contain a short-lived
	// credential.
	CallerTools *CallerToolEndpoint `json:"-"`
}

// ResolvedSandbox returns the already-resolved sandbox selection for the run.
func (c Config) ResolvedSandbox() *SandboxConfig {
	return c.SandboxSelection
}
