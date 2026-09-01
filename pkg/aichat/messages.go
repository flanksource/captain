package aichat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

// AttachmentInput is an untrusted browser file reference to be resolved into a
// prepared Captain attachment before provider execution.
type AttachmentInput struct {
	ID        string
	URL       string
	Filename  string
	MediaType string
}

// AttachmentResolver prepares browser references for provider consumption.
type AttachmentResolver interface {
	Resolve(context.Context, []AttachmentInput) ([]api.AttachmentRef, error)
}

type partLocation struct{ message, part int }

func (s *Service) resolveAttachments(ctx context.Context, messages []UIMessage) (map[partLocation]api.AttachmentRef, error) {
	inputs := make([]AttachmentInput, 0)
	locations := make([]partLocation, 0)
	for messageIndex, message := range messages {
		for partIndex, part := range message.Parts {
			if part.Type != "file" {
				continue
			}
			if message.Role != string(api.RoleUser) {
				return nil, fmt.Errorf("message %d: file parts require user role", messageIndex+1)
			}
			inputs = append(inputs, AttachmentInput{
				ID: part.AttachmentID, URL: part.URL, Filename: part.Filename, MediaType: part.MediaType,
			})
			locations = append(locations, partLocation{message: messageIndex, part: partIndex})
		}
	}
	if len(inputs) == 0 {
		return nil, nil
	}
	if s.options.Attachments == nil {
		return nil, fmt.Errorf("chat attachments require an attachment resolver")
	}
	refs, err := s.options.Attachments.Resolve(ctx, inputs)
	if err != nil {
		return nil, fmt.Errorf("resolve chat attachments: %w", err)
	}
	if len(refs) != len(inputs) {
		return nil, fmt.Errorf("attachment resolver returned %d results for %d inputs", len(refs), len(inputs))
	}
	resolved := make(map[partLocation]api.AttachmentRef, len(refs))
	for i, ref := range refs {
		if err := ref.Validate(); err != nil {
			return nil, fmt.Errorf("resolved attachment %d: %w", i+1, err)
		}
		if !ref.IsPrepared() {
			return nil, fmt.Errorf("resolved attachment %d is not prepared", i+1)
		}
		resolved[locations[i]] = ref
	}
	return resolved, nil
}

func requestSpec(request ChatRequest, profile RuntimeProfile, attachments map[partLocation]api.AttachmentRef) (api.ResolvedSpec, error) {
	override, err := chatModel(request, profile.Resolved.Spec.Model)
	if err != nil {
		return api.ResolvedSpec{}, err
	}
	// A requested posture is a permissions concern, so it no longer has to
	// conjure a sandbox to carry it — the profile's isolation setting stands.
	user := api.SpecLayer{Name: "chat request", Scope: api.SpecLayerUser, Spec: api.Spec{
		Model:           override,
		Budget:          request.Budget,
		ToolPreferences: request.ToolPreferences,
		ToolApproval:    request.ToolApproval,
		Permissions:     api.Permissions{Mode: request.PermissionMode},
		SessionID:       request.ProviderSessionID,
	}}
	layers := append([]api.SpecLayer(nil), profile.Resolved.Trace...)
	resolved, err := api.ResolveSpecLayers(append(layers, user)...)
	if err != nil {
		return api.ResolvedSpec{}, fmt.Errorf("resolve chat runtime profile: %w", err)
	}
	spec := resolved.Spec
	baseSystem := strings.TrimSpace(strings.Join([]string{
		profile.System, spec.Prompt.System, spec.Prompt.AppendSystem,
	}, "\n\n"))
	spec.Prompt.User = ""
	spec.Prompt.System = ""
	spec.Prompt.AppendSystem = ""
	spec.Prompt.Attachments = nil
	if request.ToolApproval == nil {
		messages, err := canonicalMessages(request.Messages, attachments)
		if err != nil {
			return api.ResolvedSpec{}, err
		}
		system, err := requestSystem(baseSystem, request)
		if err != nil {
			return api.ResolvedSpec{}, err
		}
		if spec.Mode == api.ModeAgent {
			user, promptAttachments, err := agentPrompt(messages, request.ProviderSessionID != "")
			if err != nil {
				return api.ResolvedSpec{}, err
			}
			spec.Messages = nil
			spec.Prompt.System = system
			spec.Prompt.User = user
			spec.Prompt.Attachments = promptAttachments
		} else {
			if system != "" {
				messages = append([]api.Message{{Role: api.RoleSystem, Parts: []api.Part{{Type: api.PartText, Text: system}}}}, messages...)
			}
			spec.Messages = messages
		}
	} else {
		spec.Messages = nil
	}
	if err := spec.Validate(); err != nil {
		return api.ResolvedSpec{}, err
	}
	resolved.Spec = spec
	return resolved, nil
}

func chatModel(request ChatRequest, fallback api.Model) (api.Model, error) {
	selected := fallback
	if request.Runtime != nil {
		selected = *request.Runtime
	} else if model := strings.TrimSpace(request.Model); model != "" {
		selected = api.Model{Name: model}
	}
	if strings.TrimSpace(selected.Name) == "" {
		return api.Model{}, fmt.Errorf("chat model is required")
	}
	if request.ReasoningEffort != "" {
		if selected.Effort != "" && selected.Effort != request.ReasoningEffort {
			return api.Model{}, fmt.Errorf("chat runtime effort %q conflicts with reasoning effort %q", selected.Effort, request.ReasoningEffort)
		}
		selected.Effort = request.ReasoningEffort
	}
	if request.Temperature != nil {
		if selected.Temperature != nil && *selected.Temperature != *request.Temperature {
			return api.Model{}, fmt.Errorf("chat runtime temperature conflicts with request temperature")
		}
		selected.Temperature = request.Temperature
	}
	// Resolve, not just Expand: the browser sends the chat catalog's id form
	// ("anthropic/claude-opus-5"), which Expand leaves untouched because it
	// carries no compact-form ":" separator — keeping the provider glued to the
	// name and the mode empty for the configured default to claim.
	resolved, err := ai.Resolve(selected)
	if err != nil {
		return api.Model{}, fmt.Errorf("invalid chat runtime: %w", err)
	}
	if request.Runtime != nil && strings.TrimSpace(request.Model) != "" {
		// The catalog id names a model, never a mode, so it is resolved under the
		// structured runtime's mode. Resolving it bare would take the provider's
		// default instead and read as a conflict with the very runtime the client
		// authored — the two fields would contradict each other by construction.
		bare, err := ai.Resolve(api.Model{Name: strings.TrimSpace(request.Model), Mode: resolved.Mode})
		if err != nil {
			return api.Model{}, fmt.Errorf("invalid chat model %q: %w", request.Model, err)
		}
		if bare.Name != resolved.Name || bare.Provider != resolved.Provider {
			return api.Model{}, fmt.Errorf("chat model %q conflicts with structured runtime %s/%s", request.Model, api.RuntimeOf(resolved.Provider, resolved.Mode), resolved.Name)
		}
	}
	return resolved, nil
}

func canonicalMessages(messages []UIMessage, attachments map[partLocation]api.AttachmentRef) ([]api.Message, error) {
	out := make([]api.Message, 0, len(messages))
	for messageIndex, message := range messages {
		role := api.MessageRole(message.Role)
		parts := make([]api.Part, 0, len(message.Parts))
		results := make([]api.Part, 0)
		for partIndex, part := range message.Parts {
			mapped, result, err := canonicalPart(role, part, attachments[partLocation{message: messageIndex, part: partIndex}])
			if err != nil {
				return nil, fmt.Errorf("message %d part %d: %w", messageIndex+1, partIndex+1, err)
			}
			if mapped != nil {
				parts = append(parts, *mapped)
			}
			if result != nil {
				results = append(results, *result)
			}
		}
		if len(parts) == 0 {
			if role == api.RoleAssistant {
				continue
			}
			return nil, fmt.Errorf("message %d (%s) has no provider content", messageIndex+1, role)
		}
		out = append(out, api.Message{Role: role, Parts: parts})
		if len(results) > 0 {
			out = append(out, api.Message{Role: api.RoleTool, Parts: results})
		}
	}
	return out, nil
}

func canonicalPart(role api.MessageRole, part UIPart, attachment api.AttachmentRef) (*api.Part, *api.Part, error) {
	switch {
	case part.Type == "text":
		return &api.Part{Type: api.PartText, Text: part.Text}, nil, nil
	case part.Type == "reasoning":
		return &api.Part{Type: api.PartReasoning, Text: part.Text}, nil, nil
	case part.Type == "file":
		return &api.Part{Type: api.PartAttachment, Attachment: &attachment}, nil, nil
	case part.IsTool():
		if role != api.RoleAssistant {
			return nil, nil, fmt.Errorf("tool parts require assistant role")
		}
		request := &api.Part{Type: api.PartToolRequest, ToolRequest: &api.ToolRequest{
			ToolCallID: part.ToolCallID, Name: part.EffectiveToolName(), Input: part.Input,
		}}
		if part.State != "output-available" && part.State != "output-error" && part.State != "output-denied" {
			return nil, nil, fmt.Errorf("tool call %q is not terminal (state %q)", part.ToolCallID, part.State)
		}
		result := &api.Part{Type: api.PartToolResult, ToolResult: &api.ToolResult{
			ToolCallID: part.ToolCallID, Output: part.Output,
		}}
		if part.State == "output-error" {
			result.ToolResult.Output = nil
			result.ToolResult.Error = part.ErrorText
		}
		if part.State == "output-denied" {
			result.ToolResult.Output = nil
			result.ToolResult.Error = "tool execution denied"
			if part.Approval != nil && part.Approval.Reason != "" {
				result.ToolResult.Error += ": " + part.Approval.Reason
			}
		}
		return request, result, nil
	case part.Type == "step-start" || strings.HasPrefix(part.Type, "data-") || strings.HasPrefix(part.Type, "source-"):
		return nil, nil, nil
	default:
		return nil, nil, fmt.Errorf("unsupported AI SDK part type %q", part.Type)
	}
}

func requestSystem(base string, request ChatRequest) (string, error) {
	sections := make([]string, 0, 2)
	if strings.TrimSpace(base) != "" {
		sections = append(sections, strings.TrimSpace(base))
	}
	contextSections := make([]string, 0, 2)
	if strings.TrimSpace(request.Context) != "" {
		contextSections = append(contextSections, strings.TrimSpace(request.Context))
	}
	if len(request.ContextItems) > 0 {
		payload, err := json.Marshal(request.ContextItems)
		if err != nil {
			return "", fmt.Errorf("marshal chat context items: %w", err)
		}
		contextSections = append(contextSections, "Structured context items JSON:\n"+string(payload))
	}
	if len(contextSections) > 0 {
		sections = append(sections, "Current UI context:\n"+strings.Join(contextSections, "\n"))
	}
	return strings.Join(sections, "\n\n"), nil
}
