package genkit

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"

	gkai "github.com/firebase/genkit/go/ai"
)

// bareModel strips a leading provider prefix so a model id can be re-prefixed for
// either a genkit ref (anthropic/openai/googleai) or an OpenRouter pricing key.
func bareModel(model string) string {
	for _, prefix := range []string{"anthropic/", "openai/", "googleai/", "google/", "deepseek/", "models/"} {
		if strings.HasPrefix(model, prefix) {
			return strings.TrimPrefix(model, prefix)
		}
	}
	return model
}

// modelRef produces the genkit model reference for a backend+model
// (anthropic/<model>, openai/<model>, googleai/<model>).
func modelRef(backend ai.Backend, model string) (string, error) {
	if model == "" {
		return "", fmt.Errorf("genkit provider: model cannot be empty")
	}
	bare := bareModel(model)
	switch backend {
	case ai.BackendAnthropic:
		return "anthropic/" + bare, nil
	case ai.BackendOpenAI:
		return "openai/" + bare, nil
	case ai.BackendGemini:
		return "googleai/" + bare, nil
	case ai.BackendDeepSeek:
		return "deepseek/" + bare, nil
	default:
		return "", fmt.Errorf("genkit provider: unsupported backend %q", backend)
	}
}

// generateOptions assembles the genkit Generate options for one turn: model,
// system prompt, user prompt, effort config, and (when streaming) the callback.
// WithOutputType is added only for the non-streaming structured-output path;
// ExecuteStream rejects structured output before calling this.
func generateOptions(p *Provider, req ai.Request, stream gkai.ModelStreamCallback) ([]gkai.GenerateOption, error) {
	opts := []gkai.GenerateOption{gkai.WithModelName(p.modelRef)}
	if p.cfg.Model.Name != "" {
		req.Name = p.cfg.Model.Name
	}
	if p.cfg.Model.ID != "" && req.ID == "" {
		req.ID = p.cfg.Model.ID
	}

	if req.Prompt.System != "" {
		opts = append(opts, gkai.WithSystem(req.Prompt.System))
	}
	opts = append(opts, gkai.WithPrompt(req.Prompt.User))

	modelToken := req.Name
	if modelToken == "" {
		modelToken = req.ID
	}
	if cfg := ai.EffortConfig(p.backend, modelToken, req.Effort, req.Budget.MaxTokens, req.Temperature); cfg != nil {
		opts = append(opts, gkai.WithConfig(cfg))
	}
	if stream != nil {
		opts = append(opts, gkai.WithStreaming(stream))
	}
	if stream == nil {
		if p.backend == ai.BackendAnthropic && req.Prompt.HasSchema() {
			raw, err := ai.SchemaJSONForBackend(p.backend, req.Prompt)
			if err != nil {
				return nil, fmt.Errorf("genkit %s: cannot derive Prompt schema: %w", p.backend, err)
			}
			var schema map[string]any
			if err := json.Unmarshal(raw, &schema); err != nil {
				return nil, fmt.Errorf("genkit %s: invalid Prompt schema: %w", p.backend, err)
			}
			opts = append(opts, gkai.WithOutputSchema(schema))
		} else if len(req.Prompt.SchemaJSON) > 0 {
			var schema map[string]any
			if err := json.Unmarshal(req.Prompt.SchemaJSON, &schema); err != nil {
				return nil, fmt.Errorf("genkit %s: invalid Prompt.SchemaJSON: %w", p.backend, err)
			}
			opts = append(opts, gkai.WithOutputSchema(schema))
		} else if req.Prompt.Schema != nil {
			opts = append(opts, gkai.WithOutputType(req.Prompt.Schema))
		}
	}
	return opts, nil
}
