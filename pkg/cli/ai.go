package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	_ "github.com/flanksource/captain/pkg/ai/provider"
)

type AIPromptOptions struct {
	Model       string        `flag:"model" help:"Model name (e.g. claude-code-sonnet, gemini-2.0-flash)" short:"m" required:"true"`
	Prompt      string        `flag:"prompt" help:"Prompt text" short:"p" required:"true" stdin:"true"`
	System      string        `flag:"system" help:"System prompt" short:"s"`
	MaxTokens   int           `flag:"max-tokens" help:"Maximum output tokens" default:"4096"`
	Temperature float64       `flag:"temperature" help:"Sampling temperature" default:"0"`
	Timeout     time.Duration `flag:"timeout" help:"Request timeout" default:"120s"`
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

	p, err := ai.NewProvider(ai.Config{Model: opts.Model})
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
	defer cancel()

	resp, err := p.Execute(ctx, ai.Request{
		SystemPrompt: opts.System,
		Prompt:       opts.Prompt,
		MaxTokens:    opts.MaxTokens,
		Temperature:  opts.Temperature,
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
	Model   string        `flag:"model" help:"Model to test" short:"m" required:"true"`
	Timeout time.Duration `flag:"timeout" help:"Request timeout" default:"30s"`
}

type AITestResult struct {
	Model   string `json:"model" pretty:"label=Model"`
	Backend string `json:"backend" pretty:"label=Backend"`
	Status  string `json:"status" pretty:"label=Status"`
	Latency string `json:"latency" pretty:"label=Latency"`
}

func RunAITest(opts AITestOptions) (any, error) {
	provider, err := ai.NewProvider(ai.Config{Model: opts.Model})
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
	defer cancel()

	start := time.Now()
	_, err = provider.Execute(ctx, ai.Request{
		Prompt:    "Respond with exactly: ok",
		MaxTokens: 10,
	})

	result := AITestResult{
		Model:   provider.GetModel(),
		Backend: string(provider.GetBackend()),
		Latency: time.Since(start).Round(time.Millisecond).String(),
	}

	if err != nil {
		result.Status = fmt.Sprintf("FAIL: %v", err)
	} else {
		result.Status = "OK"
	}
	return result, nil
}
