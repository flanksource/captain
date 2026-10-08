package claudeagent

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/provider/jsonrpc"
	"github.com/flanksource/captain/pkg/api"
)

// methodElicit is the server→client request agent.ts sends when an MCP server
// asks the person for input through the SDK's onElicitation.
const methodElicit = "elicit"

const elicitationTool = "Elicitation"

// elicitAction is the MCP ElicitResult action.
type elicitAction string

const (
	elicitAccept  elicitAction = "accept"
	elicitDecline elicitAction = "decline"
	elicitCancel  elicitAction = "cancel"
)

// elicitParams is the agent.ts elicit request: the SDK ElicitationRequest plus
// the bridge instance and a per-instance request counter, which together key
// the approval because the counter restarts with the agent.ts child.
type elicitParams struct {
	BridgeID        string         `json:"bridgeId"`
	RequestID       int64          `json:"requestId"`
	ServerName      string         `json:"serverName"`
	Message         string         `json:"message"`
	Mode            string         `json:"mode"`
	RequestedSchema map[string]any `json:"requestedSchema,omitempty"`
	URL             string         `json:"url,omitempty"`
	ElicitationID   string         `json:"elicitationId,omitempty"`
}

// elicitResult is the MCP ElicitResult agent.ts hands back to the SDK.
type elicitResult struct {
	Action  elicitAction   `json:"action"`
	Content map[string]any `json:"content,omitempty"`
}

// handleElicit answers an MCP elicitation through OnApproval. The deny
// pre-check does not apply: an elicitation asks for input, it runs no tool.
// With no callback it declines, as the SDK does when onElicitation is unset;
// with no active turn, a failed callback, or an invalid decision it cancels.
func (p *Provider) handleElicit(params json.RawMessage) (any, *jsonrpc.RPCError) {
	request, err := elicitationRequest(params, p.currentSessionID())
	if err != nil {
		return nil, &jsonrpc.RPCError{Code: -32602, Message: "invalid elicit params: " + err.Error()}
	}
	if p.cfg.OnApproval == nil {
		return elicitResult{Action: elicitDecline}, nil
	}
	ts := p.activeTurn()
	if ts == nil {
		log.Warnf("claude-agent: no active turn to answer elicitation %s from %q; cancelled", request.ToolUseID, request.Elicitation.Server)
		return elicitResult{Action: elicitCancel}, nil
	}
	decision, err := ts.onApproval(ts.ctx, request)
	switch {
	case errors.Is(err, api.ErrLegacyApprovalSkipped):
		api.LogLegacyApprovalSkip(request.SessionID, api.ApprovalKindElicitation, err, string(elicitDecline))
		return elicitResult{Action: elicitDecline}, nil
	case err != nil:
		log.Warnf("claude-agent: elicitation %s from %q cancelled: %v", request.ToolUseID, request.Elicitation.Server, err)
		return elicitResult{Action: elicitCancel}, nil
	}
	if err := decision.Validate(request); err != nil {
		log.Warnf("claude-agent: elicitation %s from %q cancelled on an invalid decision: %v", request.ToolUseID, request.Elicitation.Server, err)
		return elicitResult{Action: elicitCancel}, nil
	}
	switch {
	case decision.Allow:
		return elicitResult{Action: elicitAccept, Content: decision.UpdatedInput}, nil
	case decision.Interrupt:
		return elicitResult{Action: elicitCancel}, nil
	}
	return elicitResult{Action: elicitDecline}, nil
}

func elicitationRequest(raw json.RawMessage, sessionID string) (ai.ApprovalRequest, error) {
	var params elicitParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return ai.ApprovalRequest{}, err
	}
	switch {
	case params.BridgeID == "":
		return ai.ApprovalRequest{}, fmt.Errorf("bridgeId is required")
	case params.RequestID <= 0:
		return ai.ApprovalRequest{}, fmt.Errorf("requestId must be positive, got %d", params.RequestID)
	case params.ServerName == "":
		return ai.ApprovalRequest{}, fmt.Errorf("serverName is required")
	}
	mode := api.ElicitationMode(params.Mode)
	if mode != api.ElicitationModeForm && mode != api.ElicitationModeURL {
		return ai.ApprovalRequest{}, fmt.Errorf("mode %q is neither form nor url", params.Mode)
	}
	input := map[string]any{"serverName": params.ServerName, "message": params.Message, "mode": params.Mode}
	if params.RequestedSchema != nil {
		input["requestedSchema"] = params.RequestedSchema
	}
	if params.URL != "" {
		input["url"] = params.URL
	}
	if params.ElicitationID != "" {
		input["elicitationId"] = params.ElicitationID
	}
	request := ai.ApprovalRequest{
		Tool: elicitationTool, Input: input, SessionID: sessionID,
		ToolUseID: fmt.Sprintf("elicit:%s:%d", params.BridgeID, params.RequestID),
		Kind:      api.ApprovalKindElicitation, Interruptible: true, SupportedScopes: claudeApprovalScopes,
		Elicitation: &api.ElicitationApproval{
			Server: params.ServerName, Mode: mode, Message: params.Message,
			Schema: params.RequestedSchema, URL: params.URL, ElicitationID: params.ElicitationID,
		},
	}
	return request, request.Validate()
}
