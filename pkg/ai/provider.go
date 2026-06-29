package ai

import (
	"context"
	"fmt"
	"strings"
)

type Backend string

const (
	BackendAnthropic Backend = "anthropic"
	BackendGemini    Backend = "gemini"
	BackendOpenAI    Backend = "openai"
	BackendClaudeCLI Backend = "claude-cli"
	BackendCodexCLI  Backend = "codex-cli"
	BackendGeminiCLI Backend = "gemini-cli"
	// BackendClaudeAgent runs the Claude Agent SDK as a long-lived clicky-supervised
	// TS process spoken to over JSON-RPC stdio. It replaces the one-shot claude_cli
	// (`claude -p`) path for agentic, multi-turn, tool-using runs.
	BackendClaudeAgent Backend = "claude-agent"
)

// AllBackends lists every supported backend in canonical order. It is the single
// source of truth behind Backend.Valid, BackendList, and the help/error strings
// that enumerate backends — keep new backends here and they propagate everywhere.
func AllBackends() []Backend {
	return []Backend{
		BackendAnthropic, BackendGemini, BackendOpenAI,
		BackendClaudeCLI, BackendClaudeAgent, BackendCodexCLI, BackendGeminiCLI,
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

// Kind classifies a backend as "api" (called directly over HTTP with an API
// key) or "cli" (delegated to an installed coding-agent binary that carries its
// own auth/login). Used by `captain whoami` to group adapters and decide which
// auth signals to probe.
func (b Backend) Kind() string {
	switch b {
	case BackendAnthropic, BackendGemini, BackendOpenAI:
		return "api"
	default:
		return "cli"
	}
}

// AuthEnvVars returns the environment variables consulted for a backend's API
// key, in priority order. CLI backends share their parent provider's key (the
// CLIs honour the same env var and the model-listing endpoints are the parent
// provider's), so this is the single source of truth for both NewProvider's key
// resolution and the live model listing in models_remote.go.
func AuthEnvVars(b Backend) []string {
	switch b {
	case BackendAnthropic, BackendClaudeCLI, BackendClaudeAgent:
		return []string{"ANTHROPIC_API_KEY"}
	case BackendOpenAI, BackendCodexCLI:
		return []string{"OPENAI_API_KEY"}
	case BackendGemini, BackendGeminiCLI:
		return []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}
	default:
		return nil
	}
}

// BackendList renders AllBackends as a comma-separated string for help text and
// error messages so the enumeration lives in exactly one place.
func BackendList() string {
	parts := make([]string, len(AllBackends()))
	for i, b := range AllBackends() {
		parts[i] = string(b)
	}
	return strings.Join(parts, ", ")
}

type Provider interface {
	Execute(ctx context.Context, req Request) (*Response, error)
	GetModel() string
	GetBackend() Backend
}

type StreamingProvider interface {
	Provider
	ExecuteStream(ctx context.Context, req Request) (<-chan Event, error)
}

func InferBackend(model string) (Backend, error) {
	m := strings.ToLower(model)

	// CLI backends (check before API backends to avoid prefix conflicts)
	if strings.HasPrefix(m, "claude-agent-") {
		return BackendClaudeAgent, nil
	}
	if strings.HasPrefix(m, "claude-code-") {
		return BackendClaudeCLI, nil
	}
	if strings.HasPrefix(m, "codex-") || strings.HasPrefix(m, "codex") {
		return BackendCodexCLI, nil
	}
	if strings.HasPrefix(m, "gemini-cli-") {
		return BackendGeminiCLI, nil
	}

	// API backends
	if strings.HasPrefix(m, "claude-") {
		return BackendAnthropic, nil
	}
	if strings.HasPrefix(m, "gemini-") || strings.HasPrefix(m, "models/gemini-") {
		return BackendGemini, nil
	}
	if strings.HasPrefix(m, "grok-") {
		return BackendCodexCLI, nil
	}
	if strings.HasPrefix(m, "gpt-") || strings.HasPrefix(m, "o1") || strings.HasPrefix(m, "o3") || strings.HasPrefix(m, "o4") {
		return BackendOpenAI, nil
	}

	return "", fmt.Errorf("unable to infer backend from model name: %s (pass --backend explicitly: %s)", model, BackendList())
}
