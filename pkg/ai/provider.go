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
