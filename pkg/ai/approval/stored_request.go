package approval

import (
	"encoding/json"
	"fmt"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
)

// storedRequest rebuilds the typed request a row was raised with. A row written
// before kinds existed carries only tool and input, and the only thing that was
// ever stored then was a tool approval.
func storedRequest(row *database.TurnRequest) (api.ApprovalRequest, error) {
	raw, err := json.Marshal(row.Request)
	if err != nil {
		return api.ApprovalRequest{}, fmt.Errorf("encode stored approval %s: %w", row.ID, err)
	}
	var request api.ApprovalRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return api.ApprovalRequest{}, fmt.Errorf("decode stored approval %s: %w", row.ID, err)
	}
	if request.Kind == "" {
		request.Kind = api.ApprovalKindTool
	}
	request.ToolUseID = row.ToolCallID
	return request, nil
}

// storedDecision reads the typed parts of a decision back off a resolved row.
func storedDecision(response map[string]any) (api.ApprovalDecision, error) {
	if response == nil {
		return api.ApprovalDecision{}, nil
	}
	raw, err := json.Marshal(response)
	if err != nil {
		return api.ApprovalDecision{}, fmt.Errorf("encode approval response: %w", err)
	}
	var decision api.ApprovalDecision
	if err := json.Unmarshal(raw, &decision); err != nil {
		return api.ApprovalDecision{}, fmt.Errorf("decode approval response: %w", err)
	}
	return decision, nil
}
