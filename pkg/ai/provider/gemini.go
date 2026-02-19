package provider

import (
	"context"
	"fmt"

	"github.com/flanksource/captain/pkg/ai"
)

// FIXME: implement Gemini API provider using google genai SDK
type Gemini struct {
	model  string
	apiKey string
}

func NewGemini(model, apiKey string) *Gemini {
	if model == "" {
		model = "gemini-2.0-flash"
	}
	return &Gemini{model: model, apiKey: apiKey}
}

func (g *Gemini) GetModel() string      { return g.model }
func (g *Gemini) GetBackend() ai.Backend { return ai.BackendGemini }

func (g *Gemini) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	return nil, fmt.Errorf("Gemini API provider not yet implemented")
}
