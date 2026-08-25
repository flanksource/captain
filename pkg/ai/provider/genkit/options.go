package genkit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/observation"
	captools "github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"

	gkai "github.com/firebase/genkit/go/ai"
)

// bareModel strips a leading provider prefix so a model id can be re-prefixed for
// either a genkit ref or an OpenRouter pricing key.
func bareModel(model string) string {
	return registry.StripProviderPrefix(model)
}

// modelRef produces the genkit model reference for a backend+model
// (anthropic/<model>, openai/<model>, googleai/<model>).
//
// The namespace is the provider's CatalogPrefix — googleai for Gemini — which is
// deliberately not the same field pricing uses (google). Keeping them as one
// hand-written switch each is how the two drifted apart.
func modelRef(backend ai.Backend, model string) (string, error) {
	if model == "" {
		return "", fmt.Errorf("genkit provider: model cannot be empty")
	}
	// genkit serves the API modes only; the CLI/agent/cmux backends are driven by
	// their own providers.
	p, mode, ok := registry.ProviderFor(backend)
	if !ok || mode != registry.ModeAPI {
		return "", fmt.Errorf("genkit provider: unsupported backend %q", backend)
	}
	return p.CatalogPrefix + "/" + bareModel(model), nil
}

// generateOptions assembles the genkit Generate options for one turn: model,
// system prompt, user prompt, effort config, and (when streaming) the callback.
// WithOutputType is added only for the non-streaming structured-output path;
// ExecuteStream rejects structured output before calling this.
func generateOptions(p *Provider, req ai.Request, stream gkai.ModelStreamCallback, emit func(ai.Event)) ([]gkai.GenerateOption, error) {
	if err := req.ValidateRequestMode(); err != nil {
		return nil, err
	}
	opts := []gkai.GenerateOption{
		gkai.WithModelName(p.modelRef),
		gkai.WithUse(gkai.MiddlewareFunc(p.captureGenkitRequests)),
	}
	toolOptions, err := p.toolOptions(captools.ResolveOptions{Preferences: req.ToolPreferences, Policy: req.ToolPolicy}, emit)
	if err != nil {
		return nil, err
	}
	opts = append(opts, toolOptions...)
	if p.cfg.Model.Name != "" {
		req.Name = p.cfg.Model.Name
	}
	if p.cfg.Model.ID != "" && req.ID == "" {
		req.ID = p.cfg.Model.ID
	}

	if req.ToolApproval != nil {
		if err := ai.ValidateAttachmentCompatibility([]api.Model{req.Model}, messageAttachments(req.ToolApproval.State.Messages)); err != nil {
			return nil, err
		}
		messages, restarts, responses, err := prepareToolApprovalResume(req.ToolApproval)
		if err != nil {
			return nil, fmt.Errorf("genkit tool approval resume: %w", err)
		}
		opts = append(opts, gkai.WithMessages(messages...))
		if len(restarts) > 0 {
			opts = append(opts, gkai.WithToolRestarts(restarts...))
		}
		if len(responses) > 0 {
			opts = append(opts, gkai.WithToolResponses(responses...))
		}
	} else if len(req.Messages) > 0 {
		if err := req.ValidateRequestMode(); err != nil {
			return nil, err
		}
		if err := ai.ValidateAttachmentCompatibility([]api.Model{req.Model}, messageAttachments(req.Messages)); err != nil {
			return nil, err
		}
		messages, err := conversationMessages(req.Messages)
		if err != nil {
			return nil, err
		}
		opts = append(opts, gkai.WithMessages(messages...))
	} else {
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
	}

	modelToken := req.Name
	if modelToken == "" {
		modelToken = req.ID
	}
	cfg := ai.EffortConfig(p.backend, modelToken, req.Effort, req.Budget.MaxTokens, req.Temperature)
	model := bareModel(modelToken)
	if p.backend == ai.BackendOpenAI && len(toolOptions) > 0 && (model == "gpt-5.6" || strings.HasPrefix(model, "gpt-5.6-")) {
		if cfg == nil {
			cfg = map[string]any{}
		}
		cfg["reasoning_effort"] = "none"
	}
	if cfg != nil {
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

type genkitToolRequestContextKey struct{}

func (p *Provider) captureGenkitRequests(context.Context) (*gkai.Hooks, error) {
	return &gkai.Hooks{
		WrapModel: func(ctx context.Context, params *gkai.ModelParams, next gkai.ModelNext) (*gkai.ModelResponse, error) {
			if p.backend == ai.BackendOpenAI && params != nil && params.Request != nil {
				config, _ := params.Request.Config.(map[string]any)
				effort, present := config["reasoning_effort"].(string)
				observation.RecordReasoningDispatch(ctx, "openai.chat.completions.create", present, effort)
			}
			response, err := next(ctx, params)
			if response != nil {
				response.Request = &gkai.ModelRequest{Messages: cloneCheckpointMessages(params.Request.Messages)}
			}
			return response, err
		},
		WrapTool: func(ctx context.Context, params *gkai.ToolParams, next gkai.ToolNext) (*gkai.MultipartToolResponse, error) {
			if params == nil || params.Request == nil {
				return nil, fmt.Errorf("genkit tool middleware received no provider request")
			}
			return next(context.WithValue(ctx, genkitToolRequestContextKey{}, params.Request), params)
		},
	}, nil
}

func promptParts(req ai.Request) ([]*gkai.Part, error) {
	parts := make([]*gkai.Part, 0, len(req.Prompt.Attachments))
	if req.Prompt.User != "" {
		parts = append(parts, gkai.NewTextPart(req.Prompt.User))
	}
	for i, attachment := range req.Prompt.Attachments {
		part, err := preparedAttachmentPart(attachment, fmt.Sprintf("attachment %d", i+1))
		if err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}
	return parts, nil
}

func conversationMessages(messages []api.Message) ([]*gkai.Message, error) {
	if err := api.ValidateMessages(messages); err != nil {
		return nil, fmt.Errorf("canonical messages: %w", err)
	}
	toolNames := map[string]string{}
	out := make([]*gkai.Message, 0, len(messages))
	for i, message := range messages {
		parts := make([]*gkai.Part, 0, len(message.Parts))
		for j, part := range message.Parts {
			switch part.Type {
			case api.PartText:
				parts = append(parts, gkai.NewTextPart(part.Text))
			case api.PartReasoning:
				parts = append(parts, gkai.NewReasoningPart(part.Text, nil))
			case api.PartAttachment:
				media, err := preparedAttachmentPart(*part.Attachment, fmt.Sprintf("message %d part %d attachment", i+1, j+1))
				if err != nil {
					return nil, err
				}
				parts = append(parts, media)
			case api.PartToolRequest:
				input, err := decodePartJSON(part.ToolRequest.Input)
				if err != nil {
					return nil, fmt.Errorf("message %d part %d tool request input: %w", i+1, j+1, err)
				}
				toolNames[part.ToolRequest.ToolCallID] = part.ToolRequest.Name
				parts = append(parts, gkai.NewToolRequestPart(&gkai.ToolRequest{Ref: part.ToolRequest.ToolCallID, Name: part.ToolRequest.Name, Input: input}))
			case api.PartToolResult:
				output, err := decodePartJSON(part.ToolResult.Output)
				if err != nil {
					return nil, fmt.Errorf("message %d part %d tool result output: %w", i+1, j+1, err)
				}
				if part.ToolResult.Error != "" {
					output = map[string]any{"error": part.ToolResult.Error}
				}
				parts = append(parts, gkai.NewToolResponsePart(&gkai.ToolResponse{Ref: part.ToolResult.ToolCallID, Name: toolNames[part.ToolResult.ToolCallID], Output: output}))
			}
		}
		out = append(out, gkai.NewMessage(genkitRole(message.Role), nil, parts...))
	}
	return out, nil
}

func genkitRole(role api.MessageRole) gkai.Role {
	switch role {
	case api.RoleSystem:
		return gkai.RoleSystem
	case api.RoleUser:
		return gkai.RoleUser
	case api.RoleAssistant:
		return gkai.RoleModel
	case api.RoleTool:
		return gkai.RoleTool
	default:
		return ""
	}
}

func decodePartJSON(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func messageAttachments(messages []api.Message) []api.AttachmentRef {
	var attachments []api.AttachmentRef
	for _, message := range messages {
		for _, part := range message.Parts {
			if part.Type == api.PartAttachment && part.Attachment != nil {
				attachments = append(attachments, *part.Attachment)
			}
		}
	}
	return attachments
}

func preparedAttachmentPart(attachment api.AttachmentRef, label string) (*gkai.Part, error) {
	content, ok := attachment.PreparedContent()
	if !ok {
		return nil, fmt.Errorf("%s (%s) is not prepared", label, attachment.ID)
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
	return gkai.NewMediaPart(attachment.MediaType, uri), nil
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
