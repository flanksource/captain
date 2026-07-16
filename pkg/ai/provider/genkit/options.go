package genkit

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"

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
	if len(req.Prompt.Attachments) == 0 {
		opts = append(opts, gkai.WithPrompt(req.Prompt.User))
	} else {
		if err := ai.ValidateAttachmentCompatibility([]api.Model{req.Model}, req.Prompt.Attachments); err != nil {
			return nil, err
		}
		parts, err := promptParts(req)
		if err != nil {
			return nil, err
		}
		opts = append(opts, gkai.WithMessages(gkai.NewUserMessage(parts...)))
	}

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
		if schema, handled, err := backendOutputSchema(p.backend, req); err != nil {
			return nil, err
		} else if handled {
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

func promptParts(req ai.Request) ([]*gkai.Part, error) {
	parts := make([]*gkai.Part, 0, len(req.Prompt.Attachments)+1)
	if req.Prompt.User != "" {
		parts = append(parts, gkai.NewTextPart(req.Prompt.User))
	}
	for i, attachment := range req.Prompt.Attachments {
		content, ok := attachment.PreparedContent()
		if !ok {
			return nil, fmt.Errorf("attachment %d (%s) is not prepared", i+1, attachment.ID)
		}
		data := content.Bytes
		if data == nil && content.Path != "" {
			var err error
			data, err = os.ReadFile(content.Path)
			if err != nil {
				return nil, fmt.Errorf("read prepared attachment %s: %w", attachment.ID, err)
			}
		}
		uri := "data:" + attachment.MediaType + ";base64," + base64.StdEncoding.EncodeToString(data)
		parts = append(parts, gkai.NewMediaPart(attachment.MediaType, uri))
	}
	return parts, nil
}

// backendOutputSchema resolves schemas for native providers whose supported
// JSON Schema subset differs from Captain's caller-facing schema. The bool is
// false for backends that should retain Genkit's existing WithOutputType or raw
// SchemaJSON behavior.
func backendOutputSchema(backend ai.Backend, req ai.Request) (map[string]any, bool, error) {
	if !req.Prompt.HasSchema() || (!ai.UsesAnthropicSchemaSubset(backend) && !ai.UsesOpenAISchemaSubset(backend)) {
		return nil, false, nil
	}
	raw, err := ai.SchemaJSONForBackend(backend, req.Prompt)
	if err != nil {
		return nil, true, fmt.Errorf("genkit %s: cannot derive Prompt schema: %w", backend, err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, true, fmt.Errorf("genkit %s: invalid Prompt schema: %w", backend, err)
	}
	return schema, true, nil
}
