package genkit

import (
	gkai "github.com/firebase/genkit/go/ai"
	"github.com/flanksource/captain/pkg/ai"
)

func anthropicModels() map[string]gkai.ModelOptions {
	models := make(map[string]gkai.ModelOptions)
	for _, model := range ai.Anthropic.Models() {
		models[model.ID] = gkai.ModelOptions{
			Label: model.Label,
			Supports: &gkai.ModelSupports{
				Multiturn: true, Tools: true, ToolChoice: true, SystemRole: true, Media: true,
				Output: []string{"text", "json"}, Constrained: gkai.ConstrainedSupportAll,
			},
		}
	}
	return models
}
