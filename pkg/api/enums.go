// Package api holds captain's root domain types — the serializable, nested
// specification of a model/agent run (Model, Cost, Budget, Memory, Permissions,
// Setup, Prompt) and the Spec that composes them. It never imports pkg/ai;
// pkg/ai re-exports the enum/value types here via aliases, so this package is
// the single source of truth for Backend/Effort/Cost.
package api

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInferBackend marks the "can't infer a backend from this model name" failure
// so callers can enrich it (e.g. with "did you mean" model suggestions).
var ErrInferBackend = errors.New("unable to infer backend from model name")

// Backend is the provider/runtime that serves a request. This is the canonical
// definition; pkg/ai re-exports it via `type Backend = api.Backend`.
type Backend string

const (
	BackendAnthropic   Backend = "anthropic"
	BackendGemini      Backend = "gemini"
	BackendOpenAI      Backend = "openai"
	BackendDeepSeek    Backend = "deepseek"
	BackendClaudeCLI   Backend = "claude-cli"
	BackendCodexCLI    Backend = "codex-cli"
	BackendGeminiCLI   Backend = "gemini-cli"
	BackendClaudeAgent Backend = "claude-agent"
	// BackendClaudeCmux / BackendCodexCmux drive an interactive claude/codex TUI
	// inside a tmux/cmux surface (the cmux provider), tailing the session JSONL.
	// They are selected explicitly, not inferred from a model name.
	BackendClaudeCmux Backend = "claude-cmux"
	BackendCodexCmux  Backend = "codex-cmux"
)

// AllBackends lists every supported backend in canonical order — the single
// source of truth behind Valid, BackendList, and the help/error strings.
func AllBackends() []Backend {
	return []Backend{
		BackendAnthropic, BackendGemini, BackendOpenAI, BackendDeepSeek,
		BackendClaudeCLI, BackendClaudeAgent, BackendClaudeCmux,
		BackendCodexCLI, BackendCodexCmux, BackendGeminiCLI,
	}
}

// Valid reports whether b is one of the supported backends.
func (b Backend) Valid() bool {
	for _, x := range AllBackends() {
		if b == x {
			return true
		}
	}
	return false
}

// Kind classifies a backend as "api" (called directly over HTTP with an API key)
// or "cli" (delegated to an installed coding-agent binary with its own auth).
func (b Backend) Kind() string {
	switch b {
	case BackendAnthropic, BackendGemini, BackendOpenAI, BackendDeepSeek:
		return "api"
	default:
		return "cli"
	}
}

// AuthEnvVars returns the environment variables consulted for a backend's API
// key, in priority order. Some CLI backends can use a parent provider key, while
// cmux backends are keyless and rely on the local CLI login.
func AuthEnvVars(b Backend) []string {
	switch b {
	case BackendAnthropic, BackendClaudeCLI, BackendClaudeAgent:
		return []string{"ANTHROPIC_API_KEY"}
	case BackendOpenAI, BackendCodexCLI:
		return []string{"OPENAI_API_KEY"}
	case BackendDeepSeek:
		return []string{"DEEPSEEK_API_KEY"}
	case BackendGemini, BackendGeminiCLI:
		return []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}
	default:
		return nil
	}
}

// BackendList renders AllBackends as a comma-separated string for help/error text.
func BackendList() string {
	parts := make([]string, len(AllBackends()))
	for i, b := range AllBackends() {
		parts[i] = string(b)
	}
	return strings.Join(parts, ", ")
}

// InferBackend resolves the backend from a model name prefix, failing loud when
// the name matches nothing (the caller must then pass an explicit backend).
func InferBackend(model string) (Backend, error) {
	m := strings.ToLower(model)

	// CLI backends (check before API backends to avoid prefix conflicts).
	switch {
	case strings.HasPrefix(m, "claude-agent-"):
		return BackendClaudeAgent, nil
	case strings.HasPrefix(m, "claude-code-"):
		return BackendClaudeCLI, nil
	case strings.HasPrefix(m, "codex"):
		return BackendCodexCLI, nil
	case strings.HasPrefix(m, "gemini-cli-"):
		return BackendGeminiCLI, nil
	}

	switch {
	case strings.HasPrefix(m, "claude-"):
		return BackendAnthropic, nil
	case strings.HasPrefix(m, "gemini-"), strings.HasPrefix(m, "models/gemini-"):
		return BackendGemini, nil
	case strings.HasPrefix(m, "grok-"):
		return BackendCodexCLI, nil
	case strings.HasPrefix(m, "gpt-"), strings.HasPrefix(m, "o1"), strings.HasPrefix(m, "o3"), strings.HasPrefix(m, "o4"):
		return BackendOpenAI, nil
	case strings.HasPrefix(m, "deepseek"):
		return BackendDeepSeek, nil
	}

	return "", fmt.Errorf("%w: %s (pass an explicit backend: %s)", ErrInferBackend, model, BackendList())
}

// Effort is the per-request reasoning effort. captain owns this enum (it adds
// the "xhigh" tier that clicky's aichat.Effort lacks); "" means backend default.
type Effort string

const (
	EffortNone   Effort = ""
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	EffortXHigh  Effort = "xhigh"
)

// AllEfforts lists the non-empty effort tiers in ascending order.
func AllEfforts() []Effort {
	return []Effort{EffortLow, EffortMedium, EffortHigh, EffortXHigh}
}

// Valid reports whether e is a recognised effort tier (including none/"").
func (e Effort) Valid() bool {
	switch e {
	case EffortNone, EffortLow, EffortMedium, EffortHigh, EffortXHigh:
		return true
	default:
		return false
	}
}

// Validate fails loud on an unknown effort tier, naming the valid set.
func (e Effort) Validate() error {
	if e.Valid() {
		return nil
	}
	return fmt.Errorf("invalid reasoning effort %q; want one of: low, medium, high, xhigh", e)
}

// SchemaStrictness governs what captain does when a structured-output response
// fails validation against the request's JSON schema. "" disables post-response
// validation (the default — the schema is still sent to the model, just never
// checked on the way back).
type SchemaStrictness string

const (
	SchemaStrictnessNone    SchemaStrictness = ""
	SchemaStrictnessWarning SchemaStrictness = "warning"
	SchemaStrictnessError   SchemaStrictness = "error"
	SchemaStrictnessRetry   SchemaStrictness = "retry"
)

// AllSchemaStrictness lists the non-empty strictness modes.
func AllSchemaStrictness() []SchemaStrictness {
	return []SchemaStrictness{SchemaStrictnessWarning, SchemaStrictnessError, SchemaStrictnessRetry}
}

// Valid reports whether s is a recognised strictness mode (including none/"").
func (s SchemaStrictness) Valid() bool {
	switch s {
	case SchemaStrictnessNone, SchemaStrictnessWarning, SchemaStrictnessError, SchemaStrictnessRetry:
		return true
	default:
		return false
	}
}

// Validate fails loud on an unknown strictness mode, naming the valid set.
func (s SchemaStrictness) Validate() error {
	if s.Valid() {
		return nil
	}
	return fmt.Errorf("invalid schemaStrictness %q; want one of: warning, error, retry", s)
}

// VerifyScope narrows a workflow's verification to the changed files vs the
// whole tree. "" defaults to all.
type VerifyScope string

const (
	VerifyScopeAll     VerifyScope = "all"
	VerifyScopeChanged VerifyScope = "changed"
)

// AllVerifyScopes lists the non-empty verification scopes.
func AllVerifyScopes() []VerifyScope {
	return []VerifyScope{VerifyScopeAll, VerifyScopeChanged}
}

// Valid reports whether s is a recognised scope (including the default "").
func (s VerifyScope) Valid() bool {
	switch s {
	case "", VerifyScopeAll, VerifyScopeChanged:
		return true
	default:
		return false
	}
}

// Validate fails loud on an unknown scope, naming the valid set.
func (s VerifyScope) Validate() error {
	if s.Valid() {
		return nil
	}
	return fmt.Errorf("invalid verify scope %q; want one of: all, changed", s)
}

// ToolMode is the per-tool exposure for one request.
type ToolMode string

const (
	ToolModeEnabled  ToolMode = "enabled"
	ToolModeAsk      ToolMode = "ask"
	ToolModeDisabled ToolMode = "disabled"
)

// Valid reports whether m is a recognised tool mode.
func (m ToolMode) Valid() bool {
	switch m {
	case ToolModeEnabled, ToolModeAsk, ToolModeDisabled:
		return true
	default:
		return false
	}
}

// ToolPolicy is the runtime-spec policy map value for one tool. It keeps the
// wire shape close to coding-agent UX: auto, ask, allow, deny.
type ToolPolicy string

const (
	ToolPolicyAuto  ToolPolicy = "auto"
	ToolPolicyAsk   ToolPolicy = "ask"
	ToolPolicyAllow ToolPolicy = "allow"
	ToolPolicyDeny  ToolPolicy = "deny"
)

// Valid reports whether p is a recognised runtime tool policy.
func (p ToolPolicy) Valid() bool {
	switch p {
	case ToolPolicyAuto, ToolPolicyAsk, ToolPolicyAllow, ToolPolicyDeny:
		return true
	default:
		return false
	}
}

// ResourceMode is the enabled/disabled policy for MCP servers, plugins, and skills.
type ResourceMode string

const (
	ResourceEnabled  ResourceMode = "enabled"
	ResourceDisabled ResourceMode = "disabled"
)

// Valid reports whether m is a recognised resource mode.
func (m ResourceMode) Valid() bool {
	switch m {
	case ResourceEnabled, ResourceDisabled:
		return true
	default:
		return false
	}
}

// PermissionMode is the base permission posture (claude --permission-mode).
type PermissionMode string

const (
	PermissionDefault     PermissionMode = "default"
	PermissionPlan        PermissionMode = "plan"
	PermissionAcceptEdits PermissionMode = "acceptEdits"
	PermissionAuto        PermissionMode = "auto"
	PermissionBypass      PermissionMode = "bypassPermissions"
	PermissionDontAsk     PermissionMode = "dontAsk"
)

// AllPermissionModes lists the permission postures in canonical order. Mirrors
// the `claude --permission-mode` choices so the mapping is lossless.
func AllPermissionModes() []PermissionMode {
	return []PermissionMode{
		PermissionAcceptEdits, PermissionAuto, PermissionBypass, PermissionDefault, PermissionDontAsk, PermissionPlan,
	}
}

// Valid reports whether m is a recognised permission mode (including "").
func (m PermissionMode) Valid() bool {
	if m == "" {
		return true
	}
	for _, x := range AllPermissionModes() {
		if m == x {
			return true
		}
	}
	return false
}

// Preset is a named bundle of safety defaults applied before per-tool rules.
type Preset string

const (
	// PresetEdit applies acceptEdits + a curated Read/Edit/Write/Glob/Grep allowlist.
	PresetEdit Preset = "edit"
	// PresetBare skips hooks, skills, memory, and ambient settings.
	PresetBare Preset = "bare"
)

// Valid reports whether p is a recognised preset.
func (p Preset) Valid() bool {
	switch p {
	case PresetEdit, PresetBare:
		return true
	default:
		return false
	}
}
