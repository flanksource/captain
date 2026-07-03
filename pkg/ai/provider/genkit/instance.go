package genkit

import (
	"context"
	"fmt"
	"sync"

	"github.com/flanksource/captain/pkg/ai"

	"github.com/firebase/genkit/go/core/api"
	gk "github.com/firebase/genkit/go/genkit"
	"github.com/firebase/genkit/go/plugins/anthropic"
	"github.com/firebase/genkit/go/plugins/compat_oai"
	"github.com/firebase/genkit/go/plugins/compat_oai/openai"
	"github.com/firebase/genkit/go/plugins/googlegenai"
)

// deepSeekBaseURL is DeepSeek's OpenAI-compatible endpoint. The compat_oai
// client appends /chat/completions, which DeepSeek serves at this base.
const deepSeekBaseURL = "https://api.deepseek.com"

// instanceKey identifies a cached genkit instance. genkit.Init is heavy and
// registers a key-scoped plugin, so one instance is reused per (backend, apiKey).
type instanceKey struct {
	backend ai.Backend
	apiKey  string
}

// instanceEntry guards a single genkit.Init with a sync.Once so concurrent
// callers for the same key share one instance instead of racing heavy inits.
type instanceEntry struct {
	once sync.Once
	g    *gk.Genkit
	err  error
}

var instances sync.Map // instanceKey -> *instanceEntry

// getInstance returns the cached *gk.Genkit for (backend, apiKey), initializing
// it on first use. A missing API key is a loud error.
func getInstance(ctx context.Context, backend ai.Backend, apiKey string) (*gk.Genkit, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("genkit provider: missing API key for backend %q", backend)
	}

	v, _ := instances.LoadOrStore(instanceKey{backend: backend, apiKey: apiKey}, &instanceEntry{})
	entry := v.(*instanceEntry)
	entry.once.Do(func() {
		plugin, err := pluginFor(backend, apiKey)
		if err != nil {
			entry.err = err
			return
		}
		entry.g = gk.Init(ctx, gk.WithPlugins(plugin))
	})
	return entry.g, entry.err
}

// pluginFor builds the single genkit plugin for one API backend.
func pluginFor(backend ai.Backend, apiKey string) (api.Plugin, error) {
	switch backend {
	case ai.BackendAnthropic:
		return &anthropic.Anthropic{APIKey: apiKey}, nil
	case ai.BackendOpenAI:
		return &openai.OpenAI{APIKey: apiKey}, nil
	case ai.BackendGemini:
		return &googlegenai.GoogleAI{APIKey: apiKey}, nil
	case ai.BackendDeepSeek:
		// DeepSeek is OpenAI-compatible; the compat_oai plugin resolves models
		// dynamically under the "deepseek/" provider namespace.
		return &compat_oai.OpenAICompatible{Provider: "deepseek", APIKey: apiKey, BaseURL: deepSeekBaseURL}, nil
	default:
		return nil, fmt.Errorf("genkit provider: unsupported backend %q (supported: anthropic, openai, gemini, deepseek)", backend)
	}
}
