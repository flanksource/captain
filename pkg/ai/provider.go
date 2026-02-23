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
	BackendClaudeCLI Backend = "claude-cli"
	BackendCodexCLI  Backend = "codex-cli"
	BackendGeminiCLI Backend = "gemini-cli"
)

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
	if strings.HasPrefix(m, "gpt-") || strings.HasPrefix(m, "o1") || strings.HasPrefix(m, "o3") || strings.HasPrefix(m, "o4") || strings.HasPrefix(m, "grok-") {
		return BackendCodexCLI, nil
	}

	// Check if the model is in the default catalog
	for _, def := range defaultModels {
		if strings.EqualFold(def.ID, model) {
			return def.Backend, nil
		}
	}

	return "", fmt.Errorf("unable to infer backend from model name: %s (known backends: anthropic, gemini, codex-cli, claude-cli, gemini-cli)", model)
}
