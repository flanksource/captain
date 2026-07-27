// Package registry owns captain's model identity: the provider descriptors, the
// Backend/Effort/RuntimeMode enums, the Model spec type, the compact model
// grammar, and the embedded model catalog.
//
// It is a leaf — it imports nothing else from captain. That is deliberate:
// pkg/api decodes specs (and therefore parses model strings) during
// unmarshalling, so the parser cannot live above pkg/api without a cycle, and
// splitting the knowledge across both packages is exactly what let the compact
// grammar and the selector grammar drift apart. pkg/api re-exports everything
// here via aliases, so api.Model and api.Backend remain the names callers use.
package registry

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInferBackend marks the "can't infer a backend from this model name" failure
// so callers can enrich it (e.g. with "did you mean" model suggestions).
var ErrInferBackend = errors.New("unable to infer backend from model name")

// Backend is the provider/runtime that serves a request: exactly one
// (Provider, RuntimeMode) pair. This is the canonical definition; pkg/api
// re-exports it via `type Backend = registry.Backend`.
//
// Its string values are frozen — they are persisted in specs, session rows, and
// the webapp wire format.
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
	BackendCodexAgent  Backend = "codex-agent"
	// BackendClaudeCmux / BackendCodexCmux drive an interactive claude/codex TUI
	// inside a tmux/cmux surface (the cmux provider), tailing the session JSONL.
	// They are selected explicitly, not inferred from a model name.
	BackendClaudeCmux Backend = "claude-cmux"
	BackendCodexCmux  Backend = "codex-cmux"
)

const (
	AnthropicProvider = BackendAnthropic
	OpenAIProvider    = BackendOpenAI
	GeminiProvider    = BackendGemini
	DeepSeekProvider  = BackendDeepSeek
)

// AllBackends lists every supported backend in canonical order — the single
// source of truth behind Valid, BackendList, and the help/error strings.
func AllBackends() []Backend {
	return []Backend{
		BackendAnthropic, BackendGemini, BackendOpenAI, BackendDeepSeek,
		BackendClaudeCLI, BackendClaudeAgent, BackendClaudeCmux,
		BackendCodexCLI, BackendCodexAgent, BackendCodexCmux, BackendGeminiCLI,
	}
}

// Valid reports whether b is one of the supported backends.
func (b Backend) Valid() bool {
	_, _, ok := ProviderFor(b)
	return ok
}

// Mode returns the runtime mechanism this backend represents, or "" if invalid.
func (b Backend) Mode() RuntimeMode {
	_, mode, _ := ProviderFor(b)
	return mode
}

// ModelProvider returns the descriptor for the family that owns this backend,
// or nil when the backend is invalid.
func (b Backend) ModelProvider() *Provider {
	p, _, _ := ProviderFor(b)
	return p
}

// Kind classifies a backend as "api" (called directly over HTTP with an API key)
// or "cli" (delegated to an installed coding-agent binary with its own auth).
func (b Backend) Kind() string {
	if b.Mode() == ModeAPI {
		return "api"
	}
	return "cli"
}

// Provider returns the direct API provider family that owns a runtime adapter.
// An invalid backend returns the empty value.
func (b Backend) Provider() Backend {
	p, _, ok := ProviderFor(b)
	if !ok {
		return ""
	}
	backend, err := p.BackendFor(ModeAPI)
	if err != nil {
		return ""
	}
	return backend
}

// Family returns the model family a backend serves (claude | codex | gemini |
// deepseek), or "" for an unrecognised backend.
func (b Backend) Family() string {
	p, _, ok := ProviderFor(b)
	if !ok {
		return ""
	}
	return p.AgentName
}

// AuthEnvVars returns the environment variables consulted for a backend's API
// key, in priority order. Some CLI backends can use a parent provider key, while
// cmux backends are keyless and rely on the local CLI login.
func AuthEnvVars(b Backend) []string {
	p, mode, ok := ProviderFor(b)
	if !ok {
		return nil
	}
	if caps, ok := p.Caps(mode); ok && caps.Keyless {
		return nil
	}
	return p.SupportedEnvVars()
}

// BackendList renders AllBackends as a comma-separated string for help/error text.
func BackendList() string {
	parts := make([]string, len(AllBackends()))
	for i, b := range AllBackends() {
		parts[i] = string(b)
	}
	return strings.Join(parts, ", ")
}

// InferBackend resolves the backend a model name implies, failing loud when no
// provider claims the name (the caller must then pass an explicit backend).
//
// It reads the same claim table as the rest of the parser. It used to carry its
// own prefix switch, which knew about grok but not sora or the codex codenames,
// while pkg/ai's two claim tables each knew a different subset — so the answer
// depended on which entry point you came through.
func InferBackend(model string) (Backend, error) {
	p, _, mode, ok := ProviderForToken(model)
	if !ok {
		return "", fmt.Errorf("%w: %s (pass an explicit backend: %s)", ErrInferBackend, model, BackendList())
	}
	return p.BackendFor(mode)
}
