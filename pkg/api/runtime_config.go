package api

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

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

	// OnApproval, when set, answers the approvals, questions and elicitations a
	// provider cannot answer on its own; see ApprovalFunc. Providers read it
	// through Approvals(). A nil callback keeps each provider's native
	// behaviour. It is never serialized (the agent process never sees the Go
	// closure).
	OnApproval ApprovalFunc `json:"-"`

	// CanUseTool is the pre-rename OnApproval. A callback set here runs in legacy
	// mode: it only receives the requests the old contract delivered.
	//
	// Deprecated: use OnApproval. Removed in the unified-approval Phase 6.
	CanUseTool PermissionFunc `json:"-"`
	// COMPAT(unified-approval): set by NewProvider when OnApproval came from
	// CanUseTool; read through LegacyApprovals.
	legacyApprovals bool

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
