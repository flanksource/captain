package provider

import (
	"context"
	"fmt"

	"github.com/flanksource/captain/pkg/ai"
)

// FIXME: implement Anthropic API provider using langchaingo SDK
type Anthropic struct {
	model  string
	apiKey string
}

func NewAnthropic(model, apiKey string) *Anthropic {
	if model == "" {
		model = "claude-sonnet-4"
	}
	return &Anthropic{model: model, apiKey: apiKey}
}

func (a *Anthropic) GetModel() string      { return a.model }
func (a *Anthropic) GetBackend() ai.Backend { return ai.BackendAnthropic }

func (a *Anthropic) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	return nil, fmt.Errorf("Anthropic API provider not yet implemented")
}
