package cli

import (
	"context"
	"fmt"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

type AITokensOptions struct {
	AIPromptOptions
	Method string `flag:"method" default:"estimate" help:"Token sizing method: estimate (offline) or provider (count endpoint only)" json:"method"`
}

func RunAITokens(ctx context.Context, opts AITokensOptions) (api.TokenSize, error) {
	if len(opts.MultiModels) > 0 {
		return api.TokenSize{}, fmt.Errorf("token sizing requires one model")
	}
	stdin, err := readStdinIfCLI(ctx)
	if err != nil {
		return api.TokenSize{}, err
	}
	rendered, err := renderPromptCLI(ctx, opts.File, opts.AIPromptOptions, opts.Vars, stdin)
	if err != nil {
		return api.TokenSize{}, err
	}
	if opts.Method == "provider" {
		if err := preparePromptAttachments(ctx, &rendered.Input, rendered.Config); err != nil {
			return api.TokenSize{}, err
		}
	}
	return ai.SizeTokens(ctx, ai.TokenSizeOptions{Request: rendered.Input, Config: rendered.Config, Method: opts.Method})
}
