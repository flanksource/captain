package genkit

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"google.golang.org/genai"
)

type geminiTokenBody struct {
	Model             string           `json:"model"`
	Contents          []*genai.Content `json:"contents"`
	SystemInstruction *genai.Content   `json:"systemInstruction,omitempty"`
	Tools             []map[string]any `json:"tools,omitempty"`
	GenerationConfig  map[string]any   `json:"generationConfig,omitempty"`
}

func geminiCountBody(req api.Spec, model string, messages []api.Message, defs []api.ToolDefinition) (geminiTokenBody, []string, error) {
	body := geminiTokenBody{Model: "models/" + strings.TrimPrefix(model, "models/")}
	system := tokenSystem(req)
	var excluded []string
	names := map[string]string{}
	for _, msg := range messages {
		if msg.Role == api.RoleSystem {
			for _, part := range msg.Parts {
				system = strings.Join(nonemptyTokenText(system, part.Text), "\n\n")
			}
			continue
		}
		role := "user"
		if msg.Role == api.RoleAssistant {
			role = "model"
		}
		native := &genai.Content{Role: role}
		for _, part := range msg.Parts {
			projected, missing, err := geminiTokenPart(part, names)
			if err != nil {
				return body, nil, err
			}
			if missing {
				excluded = append(excluded, "unavailable attachment content")
			}
			if projected != nil {
				native.Parts = append(native.Parts, projected)
			}
			if part.Type == api.PartReasoning {
				excluded = append(excluded, "reasoning signatures and encrypted reasoning")
			}
		}
		if len(native.Parts) > 0 {
			body.Contents = append(body.Contents, native)
		}
	}
	if len(body.Contents) == 0 {
		return body, nil, fmt.Errorf("gemini count tokens: no countable content")
	}
	if system != "" {
		body.SystemInstruction = genai.NewContentFromText(system, genai.RoleUser)
	}
	var declarations []map[string]any
	for _, def := range defs {
		declarations = append(declarations, map[string]any{"name": def.Name, "description": def.Description, "parametersJsonSchema": api.ObjectSchema(def.InputSchema)})
	}
	if len(declarations) > 0 {
		body.Tools = []map[string]any{{"functionDeclarations": declarations}}
	}
	if req.Prompt.HasSchema() {
		schema, err := ai.SchemaJSONForRuntime(ai.Google, ai.ModeAPI, req.Prompt)
		if err != nil {
			return body, nil, err
		}
		body.GenerationConfig = map[string]any{"responseMimeType": "application/json", "responseJsonSchema": json.RawMessage(schema)}
	}
	return body, excluded, nil
}

func geminiTokenPart(part api.Part, names map[string]string) (*genai.Part, bool, error) {
	switch part.Type {
	case api.PartText, api.PartReasoning:
		if part.Text == "" {
			return nil, false, nil
		}
		return &genai.Part{Text: part.Text}, false, nil
	case api.PartToolRequest:
		args := map[string]any{}
		if len(part.ToolRequest.Input) > 0 {
			if err := json.Unmarshal(part.ToolRequest.Input, &args); err != nil {
				return nil, false, err
			}
		}
		names[part.ToolRequest.ToolCallID] = part.ToolRequest.Name
		return &genai.Part{FunctionCall: &genai.FunctionCall{Name: part.ToolRequest.Name, Args: args}}, false, nil
	case api.PartToolResult:
		response := map[string]any{}
		if len(part.ToolResult.Output) > 0 {
			var value any
			if err := json.Unmarshal(part.ToolResult.Output, &value); err != nil {
				return nil, false, err
			}
			response["output"] = value
		}
		if part.ToolResult.Error != "" {
			response["error"] = part.ToolResult.Error
		}
		return &genai.Part{FunctionResponse: &genai.FunctionResponse{Name: names[part.ToolResult.ToolCallID], Response: response}}, false, nil
	case api.PartAttachment:
		data, err := tokenAttachmentBytes(*part.Attachment)
		if err != nil {
			return nil, false, err
		}
		if data == nil {
			return nil, true, nil
		}
		return &genai.Part{InlineData: &genai.Blob{MIMEType: part.Attachment.MediaType, Data: data}}, false, nil
	default:
		return nil, false, fmt.Errorf("cannot count part type %q", part.Type)
	}
}
