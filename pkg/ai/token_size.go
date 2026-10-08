package ai

import (
	"context"
	"fmt"

	"github.com/flanksource/captain/pkg/ai/pricing"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
)

type TokenSizeOptions struct {
	Request   api.Spec
	Config    api.Config
	Method    string
	Direction string
}

func SizeTokens(ctx context.Context, opts TokenSizeOptions) (api.TokenSize, error) {
	if err := ctx.Err(); err != nil {
		return api.TokenSize{}, err
	}
	if opts.Method == "" {
		opts.Method = "estimate"
	}
	if opts.Method != "estimate" && opts.Method != "provider" {
		return api.TokenSize{}, fmt.Errorf("invalid token sizing method %q: want estimate or provider", opts.Method)
	}
	if opts.Direction == "" {
		opts.Direction = "input"
	}
	if opts.Direction != "input" && opts.Direction != "output" && opts.Direction != "reasoning" {
		return api.TokenSize{}, fmt.Errorf("invalid token pricing direction %q", opts.Direction)
	}
	model, err := registry.ResolveModel(opts.Request.Model)
	if err != nil {
		return api.TokenSize{}, err
	}
	if err := opts.Request.ValidateRequestMode(); err != nil {
		return api.TokenSize{}, err
	}
	result := api.TokenSize{Model: model.Name, Source: "local-estimate"}
	var count api.TokenCount
	if opts.Method == "estimate" {
		count, err = estimateTokenRequest(opts.Request, opts.Config, model)
		result.Coverage.Excluded = append(result.Coverage.Excluded, "tokenizer and message framing")
	} else {
		count, err = countTokenRequest(ctx, opts, model)
		result.Source = "provider-count"
		if model.Provider == Anthropic {
			result.Source = "provider-estimate"
		}
		result.Coverage.FramingIncluded = true
	}
	if err != nil {
		return api.TokenSize{}, err
	}
	if count.Tokens < 0 {
		return api.TokenSize{}, fmt.Errorf("provider returned negative token count: %d", count.Tokens)
	}
	result.Coverage.Excluded = append(result.Coverage.Excluded, count.Excluded...)
	if model.Mode != ModeAPI {
		result.Coverage.Excluded = append(result.Coverage.Excluded, "hidden runtime context and built-in tools")
	}
	result.Coverage.Partial = len(result.Coverage.Excluded) > 0
	result.TotalTokens = count.Tokens
	switch opts.Direction {
	case "input":
		result.Usage.InputTokens = count.Tokens
	case "output":
		result.Usage.OutputTokens = count.Tokens
	case "reasoning":
		result.Usage.ReasoningTokens = count.Tokens
	}
	result.CostUSD = tokenSizeCost(model, result.Usage)
	return result, nil
}

func countTokenRequest(ctx context.Context, opts TokenSizeOptions, model api.Model) (api.TokenCount, error) {
	if model.Provider == DeepSeek {
		return api.TokenCount{}, fmt.Errorf("%w for %s", api.ErrTokenCountingUnsupported, model.Provider.Name)
	}
	cfg := api.Config{Model: api.Model{Name: model.Name, Mode: ModeAPI}, APIKey: opts.Config.APIKey, APIURL: opts.Config.APIURL, Tools: opts.Config.Tools}
	p, err := api.NewProvider(cfg)
	if err != nil {
		return api.TokenCount{}, err
	}
	if closer, ok := api.ProviderAs[api.CloseableProvider](p); ok {
		defer closer.Close()
	}
	counter, ok := api.ProviderAs[api.TokenCountingProvider](p)
	if !ok {
		return api.TokenCount{}, fmt.Errorf("%w for %s", api.ErrTokenCountingUnsupported, model.Provider.Name)
	}
	return counter.CountTokens(ctx, opts.Request)
}

func tokenSizeCost(model api.Model, usage api.Usage) *float64 {
	for _, id := range PricingIDs(model.Provider, model.Name) {
		if rate, ok := pricing.LookupModelInfo(id); ok {
			cost := (float64(usage.InputTokens)*rate.InputPrice + float64(usage.OutputTokens+usage.ReasoningTokens)*rate.OutputPrice) / 1e6
			return &cost
		}
	}
	return nil
}
