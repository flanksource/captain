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
	"github.com/openai/openai-go/option"
)

// deepSeekBaseURL is DeepSeek's OpenAI-compatible endpoint. The compat_oai
// client appends /chat/completions, which DeepSeek serves at this base.
const deepSeekBaseURL = "https://api.deepseek.com"

// instanceKey identifies a cached genkit instance. genkit.Init is heavy and
// registers a key-scoped plugin, so one instance is reused per
// (backend, apiKey, baseURL).
//
// baseURL is part of the key, not just the plugin: an instance built against one
// endpoint would otherwise be served from the cache to a caller that asked for a
// different one, making the override silently order-dependent.
type instanceKey struct {
	backend ai.Backend
	apiKey  string
	baseURL string
}

// instanceEntry guards a single genkit.Init with a sync.Once so concurrent
// callers for the same key share one instance instead of racing heavy inits.
type instanceEntry struct {
	once sync.Once
	g    *gk.Genkit
	err  error
}

var instances sync.Map // instanceKey -> *instanceEntry

// getInstance returns the cached *gk.Genkit for (backend, apiKey, baseURL),
// initializing it on first use. An empty baseURL means the backend's default
// endpoint. A missing API key is a loud error.
func getInstance(ctx context.Context, backend ai.Backend, apiKey, baseURL string) (*gk.Genkit, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("%w: genkit provider is missing an API key for backend %q", ai.ErrNoAPIKey, backend)
	}

	v, _ := instances.LoadOrStore(instanceKey{backend: backend, apiKey: apiKey, baseURL: baseURL}, &instanceEntry{})
	entry := v.(*instanceEntry)
	entry.once.Do(func() {
		plugin, err := pluginFor(backend, apiKey, baseURL)
		if err != nil {
			entry.err = err
			return
		}
		entry.g = gk.Init(ctx, gk.WithPlugins(plugin))
	})
	return entry.g, entry.err
}

// pluginFor builds the single genkit plugin for one API backend, pointed at
// baseURL when the caller overrode it.
func pluginFor(backend ai.Backend, apiKey, baseURL string) (api.Plugin, error) {
	switch backend {
	case ai.BackendAnthropic:
		return &anthropic.Anthropic{APIKey: apiKey, BaseURL: baseURL}, nil
	case ai.BackendOpenAI:
		plugin := &openai.OpenAI{APIKey: apiKey}
		if baseURL != "" {
			// openai.OpenAI has no BaseURL field; the endpoint is a client option.
			plugin.Opts = append(plugin.Opts, option.WithBaseURL(baseURL))
		}
		return plugin, nil
	case ai.BackendGemini:
		if baseURL != "" {
			// googlegenai exposes no endpoint override, so honouring the request
			// is impossible. Failing beats silently calling the real API with
			// what the caller believes is a redirected client.
			return nil, fmt.Errorf("genkit provider: backend %q does not support an API URL override (requested %q)", backend, baseURL)
		}
		return &googlegenai.GoogleAI{APIKey: apiKey}, nil
	case ai.BackendDeepSeek:
		// DeepSeek is OpenAI-compatible; the compat_oai plugin resolves models
		// dynamically under the "deepseek/" provider namespace.
		endpoint := baseURL
		if endpoint == "" {
			endpoint = deepSeekBaseURL
		}
		return &compat_oai.OpenAICompatible{Provider: "deepseek", APIKey: apiKey, BaseURL: endpoint}, nil
	default:
		return nil, fmt.Errorf("genkit provider: unsupported backend %q (supported: anthropic, openai, gemini, deepseek)", backend)
	}
}
