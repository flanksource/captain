package genkit

import (
	"encoding/json"
	"fmt"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"

	gkai "github.com/firebase/genkit/go/ai"
)

func toolApprovalState(req ai.Request, response *gkai.ModelResponse) (*api.ToolApprovalState, error) {
	if response == nil || response.FinishReason != gkai.FinishReasonInterrupted || response.Message == nil {
		return nil, fmt.Errorf("genkit approval state requires an interrupted model response")
	}
	messages, err := approvalRequestMessages(req)
	if err != nil {
		return nil, err
	}
	assistant, calls, err := interruptedAssistantMessage(response.Message)
	if err != nil {
		return nil, err
	}
	state := &api.ToolApprovalState{Messages: append(messages, assistant), Calls: calls}
	if err := state.Validate(); err != nil {
		return nil, fmt.Errorf("genkit approval state: %w", err)
	}
	return state, nil
}

func approvalRequestMessages(req ai.Request) ([]api.Message, error) {
	if len(req.Messages) > 0 {
		if err := api.ValidateMessages(req.Messages); err != nil {
			return nil, fmt.Errorf("canonical messages: %w", err)
		}
		return append([]api.Message(nil), req.Messages...), nil
	}
	messages := make([]api.Message, 0, 2)
	if req.Prompt.System != "" {
		messages = append(messages, api.Message{Role: api.RoleSystem, Parts: []api.Part{{Type: api.PartText, Text: req.Prompt.System}}})
	}
	parts := make([]api.Part, 0, len(req.Prompt.Attachments)+1)
	if req.Prompt.User != "" {
		parts = append(parts, api.Part{Type: api.PartText, Text: req.Prompt.User})
	}
	for i := range req.Prompt.Attachments {
		attachment := req.Prompt.Attachments[i]
		parts = append(parts, api.Part{Type: api.PartAttachment, Attachment: &attachment})
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("genkit approval state has no user prompt")
	}
	return append(messages, api.Message{Role: api.RoleUser, Parts: parts}), nil
}

func interruptedAssistantMessage(message *gkai.Message) (api.Message, []api.ToolApprovalCall, error) {
	if message.Role != gkai.RoleModel {
		return api.Message{}, nil, fmt.Errorf("genkit interrupted message has role %q, expected model", message.Role)
	}
	assistant := api.Message{Role: api.RoleAssistant, Parts: make([]api.Part, 0, len(message.Content))}
	calls := make([]api.ToolApprovalCall, 0, len(message.Content))
	for i, part := range message.Content {
		switch {
		case part.IsText():
			if part.Text != "" {
				assistant.Parts = append(assistant.Parts, api.Part{Type: api.PartText, Text: part.Text})
			}
		case part.IsReasoning():
			if part.Text != "" {
				assistant.Parts = append(assistant.Parts, api.Part{Type: api.PartReasoning, Text: part.Text})
			}
		case part.IsToolRequest() && part.ToolRequest != nil:
			call, err := interruptedApprovalCall(part)
			if err != nil {
				return api.Message{}, nil, fmt.Errorf("interrupted message part %d: %w", i+1, err)
			}
			calls = append(calls, call)
			request := call.Request
			assistant.Parts = append(assistant.Parts, api.Part{Type: api.PartToolRequest, ToolRequest: &api.ToolRequest{
				ToolCallID: request.ToolCallID, Name: request.Tool, Input: request.Input,
			}})
		default:
			return api.Message{}, nil, fmt.Errorf("interrupted message part %d has unsupported content", i+1)
		}
	}
	return assistant, calls, nil
}

func interruptedApprovalCall(part *gkai.Part) (api.ToolApprovalCall, error) {
	request := part.ToolRequest
	input, err := json.Marshal(request.Input)
	if err != nil {
		return api.ToolApprovalCall{}, fmt.Errorf("marshal tool %q input: %w", request.Name, err)
	}
	call := api.ToolApprovalCall{Request: api.ToolApprovalRequest{ToolCallID: request.Ref, Tool: request.Name, Input: input}}
	pendingOutput, resolved := part.Metadata["pendingOutput"]
	_, interrupted := part.Metadata["interrupt"]
	if resolved == interrupted {
		return api.ToolApprovalCall{}, fmt.Errorf("tool call %q must be either interrupted or resolved", request.Ref)
	}
	if resolved {
		output, err := json.Marshal(pendingOutput)
		if err != nil {
			return api.ToolApprovalCall{}, fmt.Errorf("marshal completed tool %q output: %w", request.Name, err)
		}
		call.Result = &api.ToolResult{ToolCallID: request.Ref, Output: output}
	}
	return call, nil
}

func prepareToolApprovalResume(resume *api.ToolApprovalResume) ([]*gkai.Message, []*gkai.Part, []*gkai.Part, error) {
	if resume == nil {
		return nil, nil, nil, fmt.Errorf("tool approval resume is required")
	}
	if err := resume.Validate(); err != nil {
		return nil, nil, nil, err
	}
	messages, err := conversationMessages(resume.State.Messages)
	if err != nil {
		return nil, nil, nil, err
	}
	requests := genkitApprovalRequests(messages[len(messages)-1])
	decisions := make(map[string]api.ToolApprovalDecision, len(resume.Decisions))
	for _, decision := range resume.Decisions {
		decisions[decision.ToolCallID] = decision
	}
	restarts := make([]*gkai.Part, 0, len(resume.Decisions))
	responses := make([]*gkai.Part, 0, len(resume.Decisions))
	for _, call := range resume.State.Calls {
		part := requests[call.Request.ToolCallID]
		if call.Result != nil {
			output, err := approvalResultOutput(call.Result)
			if err != nil {
				return nil, nil, nil, err
			}
			part.Metadata = map[string]any{"pendingOutput": output}
			continue
		}
		decision := decisions[call.Request.ToolCallID]
		switch decision.Action {
		case api.ToolApprovalApprove:
			input, err := approvalRestartInput(call.Request, decision)
			if err != nil {
				return nil, nil, nil, err
			}
			restart := gkai.NewToolRequestPart(&gkai.ToolRequest{Name: call.Request.Tool, Ref: call.Request.ToolCallID, Input: input})
			restart.Metadata = map[string]any{"resumed": map[string]any{"captainApproval": true}}
			restarts = append(restarts, restart)
		case api.ToolApprovalDeny:
			reason := decision.Message
			if reason == "" {
				reason = "tool call denied"
			}
			responses = append(responses, gkai.NewToolResponsePart(&gkai.ToolResponse{
				Name: call.Request.Tool, Ref: call.Request.ToolCallID, Output: map[string]any{"denied": true, "reason": reason},
			}))
		case api.ToolApprovalRespond:
			output, err := approvalResultOutput(decision.Result)
			if err != nil {
				return nil, nil, nil, err
			}
			responses = append(responses, gkai.NewToolResponsePart(&gkai.ToolResponse{Name: call.Request.Tool, Ref: call.Request.ToolCallID, Output: output}))
		}
	}
	return messages, restarts, responses, nil
}

func genkitApprovalRequests(message *gkai.Message) map[string]*gkai.Part {
	requests := make(map[string]*gkai.Part)
	for _, part := range message.Content {
		if part.IsToolRequest() && part.ToolRequest != nil {
			requests[part.ToolRequest.Ref] = part
		}
	}
	return requests
}

func approvalRestartInput(request api.ToolApprovalRequest, decision api.ToolApprovalDecision) (any, error) {
	if len(decision.Input) > 0 {
		return decodePartJSON(decision.Input)
	}
	return decodePartJSON(request.Input)
}

func approvalResultOutput(result *api.ToolResult) (any, error) {
	if result.Error != "" {
		return map[string]any{"error": result.Error}, nil
	}
	return decodePartJSON(result.Output)
}

func seedToolApprovalCorrelation(resume *api.ToolApprovalResume, correlation *toolEventCorrelation) error {
	if resume == nil || correlation == nil {
		return nil
	}
	decisions := make(map[string]api.ToolApprovalDecision, len(resume.Decisions))
	for _, decision := range resume.Decisions {
		decisions[decision.ToolCallID] = decision
	}
	for _, call := range resume.State.Calls {
		input, err := decodePartJSON(call.Request.Input)
		if err != nil {
			return fmt.Errorf("seed approval call %q: %w", call.Request.ToolCallID, err)
		}
		if decision, ok := decisions[call.Request.ToolCallID]; ok && len(decision.Input) > 0 {
			input, err = decodePartJSON(decision.Input)
			if err != nil {
				return fmt.Errorf("seed approval decision %q: %w", call.Request.ToolCallID, err)
			}
		}
		request := &gkai.ToolRequest{Name: call.Request.Tool, Ref: call.Request.ToolCallID, Input: input}
		if call.Result == nil && decisions[call.Request.ToolCallID].Action == api.ToolApprovalApprove {
			correlation.observeRequest(request)
			continue
		}
		correlation.seedResolved(request)
	}
	return nil
}
