package cli

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/middleware"
	"github.com/flanksource/captain/pkg/ai/provider"
	"github.com/flanksource/commons/logger"
)

type AIProviderOptions struct {
	Model   string `flag:"model" help:"Model name, e.g. claude-sonnet-4, gemini-2.0-flash" short:"m" required:"true"`
	Backend string `flag:"backend" help:"Force backend: anthropic, gemini, codex-cli, claude-cli, gemini-cli (default: inferred from model)" short:"b"`
	APIKey  string `flag:"api-key" help:"API key (env: ANTHROPIC_API_KEY, GEMINI_API_KEY, GOOGLE_API_KEY)"`
	NoCache bool   `flag:"no-cache" help:"Disable response caching"`
	Budget  string `flag:"budget" help:"Max spend in USD, 0=unlimited" default:"0"`
	Debug   bool   `flag:"debug" help:"Enable debug logging for HTTP requests"`
}

func (o AIProviderOptions) ToConfig() ai.Config {
	budget, _ := strconv.ParseFloat(o.Budget, 64)
	cfg := ai.Config{
		Model:     o.Model,
		Backend:   ai.Backend(o.Backend),
		APIKey:    o.APIKey,
		NoCache:   o.NoCache,
		BudgetUSD: budget,
		Debug:     o.Debug,
	}
	if o.Debug || logger.IsDebugEnabled() {
		cfg.HTTPClient = provider.NewLoggingHTTPClient()
	}
	return cfg
}

type AIPromptOptions struct {
	AIProviderOptions
	Prompt      string `flag:"prompt" help:"Prompt text" short:"p" required:"true" stdin:"true"`
	System      string `flag:"system" help:"System prompt" short:"s"`
	MaxTokens   int    `flag:"max-tokens" help:"Maximum output tokens" default:"4096"`
	Temperature string `flag:"temperature" help:"Sampling temperature" default:"0"`
	Timeout     string `flag:"timeout" help:"Request timeout" default:"120s"`
}

type AIPromptResult struct {
	Text     string `json:"text" pretty:"label=Response"`
	Model    string `json:"model" pretty:"label=Model"`
	Backend  string `json:"backend" pretty:"label=Backend"`
	Input    int    `json:"inputTokens" pretty:"label=Input Tokens"`
	Output   int    `json:"outputTokens" pretty:"label=Output Tokens"`
	Duration string `json:"duration" pretty:"label=Duration"`
}

func RunAIPrompt(opts AIPromptOptions) (any, error) {
	if opts.Prompt == "" {
		return nil, fmt.Errorf("prompt text required (use --prompt or pipe via stdin)")
	}

	p, err := ai.NewProvider(opts.ToConfig())
	if err != nil {
		return nil, err
	}
	if p, err = middleware.Wrap(p, middleware.WithLogging()); err != nil {
		return nil, err
	}

	timeout, _ := time.ParseDuration(opts.Timeout)
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	temperature, _ := strconv.ParseFloat(opts.Temperature, 64)
	resp, err := p.Execute(ctx, ai.Request{
		SystemPrompt: opts.System,
		Prompt:       opts.Prompt,
		MaxTokens:    opts.MaxTokens,
		Temperature:  temperature,
	})
	if err != nil {
		return nil, err
	}

	return AIPromptResult{
		Text:     resp.Text,
		Model:    resp.Model,
		Backend:  string(resp.Backend),
		Input:    resp.Usage.InputTokens,
		Output:   resp.Usage.OutputTokens,
		Duration: resp.Duration.Round(time.Millisecond).String(),
	}, nil
}

type AITestOptions struct {
	AIProviderOptions
	Timeout string `flag:"timeout" help:"Request timeout" default:"60s"`
}

type AITestResult struct {
	Model   string `json:"model" pretty:"label=Model"`
	Backend string `json:"backend" pretty:"label=Backend"`
	Status  string `json:"status" pretty:"label=Status"`
	Latency string `json:"latency" pretty:"label=Latency"`
}

func RunAITest(opts AITestOptions) (any, error) {
	p, err := ai.NewProvider(opts.ToConfig())
	if err != nil {
		return nil, err
	}
	if p, err = middleware.Wrap(p, middleware.WithLogging()); err != nil {
		return nil, err
	}
	timeout, _ := time.ParseDuration(opts.Timeout)
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	start := time.Now()
	_, err = p.Execute(ctx, ai.Request{
		Prompt:    "Respond with exactly: ok",
		MaxTokens: 10,
	})

	result := AITestResult{
		Model:   p.GetModel(),
		Backend: string(p.GetBackend()),
		Latency: time.Since(start).Round(time.Millisecond).String(),
	}

	if err != nil {
		result.Status = fmt.Sprintf("FAIL: %v", err)
	} else {
		result.Status = "OK"
	}
	return result, nil
}
