package provider

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/flanksource/captain/pkg/ai/provider/jsonrpc"
	"github.com/flanksource/captain/pkg/api"
)

const codexElicitationTool = "Elicitation"

func codexElicitationAnswer(action string) map[string]any {
	return map[string]any{"action": action}
}

// buildCodexElicitation asks for mcpServer/elicitation/request. An elicitation
// has no item, so its identity is the JSON-RPC request id.
func buildCodexElicitation(req jsonrpc.ServerRequest, _ *turnState) (codexApproval, error) {
	var params struct {
		ServerName      string          `json:"serverName"`
		Mode            string          `json:"mode"`
		Message         string          `json:"message"`
		RequestedSchema json.RawMessage `json:"requestedSchema"`
		URL             string          `json:"url"`
		ElicitationID   string          `json:"elicitationId"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return codexApproval{}, fmt.Errorf("codex elicitation: %w", err)
	}
	if params.ServerName == "" {
		return codexApproval{}, fmt.Errorf("codex elicitation needs a serverName")
	}
	id, err := codexRequestID(req.ID)
	if err != nil {
		return codexApproval{}, fmt.Errorf("codex elicitation from %q: %w", params.ServerName, err)
	}
	elicitation := &api.ElicitationApproval{Server: params.ServerName, Message: params.Message}
	switch params.Mode {
	case "form", "openai/form", "openaiForm":
		elicitation.Mode = api.ElicitationModeForm
		if err := json.Unmarshal(params.RequestedSchema, &elicitation.Schema); err != nil || elicitation.Schema == nil {
			return codexApproval{}, fmt.Errorf("codex elicitation from %q has a requestedSchema that is not an object: %s", params.ServerName, params.RequestedSchema)
		}
	case "url":
		if params.URL == "" {
			return codexApproval{}, fmt.Errorf("codex url elicitation from %q has no url", params.ServerName)
		}
		elicitation.Mode, elicitation.URL, elicitation.ElicitationID = api.ElicitationModeURL, params.URL, params.ElicitationID
	default:
		return codexApproval{}, fmt.Errorf("codex elicitation from %q uses mode %q, which captain cannot answer", params.ServerName, params.Mode)
	}
	input, err := codexInput(req.Params)
	if err != nil {
		return codexApproval{}, fmt.Errorf("codex elicitation: %w", err)
	}
	return codexApproval{request: api.ApprovalRequest{
		Tool: codexElicitationTool, Input: input, ToolUseID: "elicit:" + id, Kind: api.ApprovalKindElicitation,
		Interruptible: true, SupportedScopes: []api.ApprovalScope{api.ApprovalScopeRequest},
		Info: &api.ToolInfo{Name: codexElicitationTool, Parent: params.ServerName}, Elicitation: elicitation,
	}, answer: func(decision api.ApprovalDecision) (any, error) {
		return codexElicitationDecision(elicitation.Mode, decision), nil
	}}, nil
}

// codexElicitationDecision is the MCP action for a decision: form content rides
// on accept, and a url-mode accept means the person finished at the url.
func codexElicitationDecision(mode api.ElicitationMode, decision api.ApprovalDecision) map[string]any {
	switch {
	case decision.Interrupt:
		return codexElicitationAnswer("cancel")
	case !decision.Allow:
		return codexElicitationAnswer("decline")
	case mode == api.ElicitationModeForm:
		return map[string]any{"action": "accept", "content": decision.UpdatedInput}
	default:
		return codexElicitationAnswer("accept")
	}
}

// codexRequestID renders a JSON-RPC id, a string or a number, as text.
func codexRequestID(raw json.RawMessage) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var id any
	if err := decoder.Decode(&id); err != nil {
		return "", fmt.Errorf("decode JSON-RPC id %s: %w", raw, err)
	}
	switch value := id.(type) {
	case string:
		if value != "" {
			return value, nil
		}
	case json.Number:
		return value.String(), nil
	}
	return "", fmt.Errorf("JSON-RPC id %s is neither a number nor a non-empty string", raw)
}

// codexInput is a request's native params, the Input the callback sees.
func codexInput(raw json.RawMessage) (map[string]any, error) {
	var input map[string]any
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, fmt.Errorf("decode params: %w", err)
	}
	return input, nil
}
