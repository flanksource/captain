package api

import (
	"context"
	"time"
)

// PermissionFunc decides whether an agent may run a tool. It is invoked by a
// streaming provider on a can_use_tool control request; returning an error denies
// the tool with the error text fed back to the agent as the reason.
type PermissionFunc func(ctx context.Context, req PermissionRequest) (PermissionDecision, error)

// PermissionRequest describes the tool an agent wants to run. SessionID is filled
// in by the provider from the live session so a caller can key approvals by it.
type PermissionRequest struct {
	Tool      string
	Input     map[string]any
	ToolUseID string
	SessionID string
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
	// would be worse. codex-cli honours it by declaring a model_providers entry,
	// since it ignores OPENAI_BASE_URL once account auth is stored.
	APIURL string
	// Sandbox runs local agent CLI processes through sandbox-runtime. Provider
	// selection must resolve to CLI mode so the flag cannot be silently ignored.
	// It is the legacy boolean shorthand for SandboxSelection = {kind: srt}.
	Sandbox bool
	// SandboxSelection is the resolved sandbox for the run — kind, configured
	// backend name, and that backend's options. nil means unsandboxed. When both
	// this and the legacy Sandbox boolean are set, this wins.
	SandboxSelection *SandboxConfig
	CacheDBPath      string
	CacheTTL         time.Duration
	NoCache          bool
	MaxConcurrent    int
	SessionID        string
	ProjectName      string
	SchemaRepair     SchemaRepairConfig

	// CanUseTool, when set, brokers tool permissions over the stream-json control
	// protocol: the streaming provider asks this callback before a tool that needs
	// approval runs, and forwards the decision to the agent. Only providers that
	// support a server→client permission round-trip honour it (claude-agent);
	// others ignore it. A nil callback keeps the auto-approve (bypass) behaviour.
	// It is never serialized (the agent process never sees the Go closure).
	CanUseTool PermissionFunc `json:"-"`

	// Tools are caller-supplied tools exposed to the model and executed
	// in-process. Only tool-capable providers (see ToolCapableProvider — today
	// the genkit API backends) honour them; other providers, which bring their
	// own tool ecosystems, ignore the field. Never serialized (Go closures).
	Tools []ToolDefinition `json:"-"`
}

// ResolvedSandbox returns the sandbox selection for the run, folding the legacy
// boolean onto the srt kind. nil means run unsandboxed.
func (c Config) ResolvedSandbox() *SandboxConfig {
	if c.SandboxSelection != nil {
		return c.SandboxSelection
	}
	if c.Sandbox {
		return &SandboxConfig{Kind: SandboxSRT}
	}
	return nil
}
