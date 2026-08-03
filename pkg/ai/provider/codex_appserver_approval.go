package provider

import (
	"encoding/json"

	"github.com/flanksource/captain/pkg/ai/provider/jsonrpc"
)

// handleApproval auto-approves server-to-client approval requests, mirroring
// the bypass-permissions default of the exec path.
func (c *CodexAppServer) handleApproval(method string, _ json.RawMessage) (any, *jsonrpc.RPCError) {
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		return map[string]string{"decision": "accept"}, nil
	case "item/permissions/requestApproval":
		return map[string]any{"permissions": map[string]any{}, "scope": "turn"}, nil
	case "item/tool/requestUserInput":
		return map[string]any{}, nil
	default:
		return map[string]string{"decision": "approved"}, nil
	}
}
