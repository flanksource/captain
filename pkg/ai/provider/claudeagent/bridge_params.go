package claudeagent

import "encoding/json"

type initializeParams struct {
	Cwd                string                      `json:"cwd,omitempty"`
	Model              string                      `json:"model,omitempty"`
	SystemPrompt       string                      `json:"systemPrompt,omitempty"`
	AppendSystemPrompt string                      `json:"appendSystemPrompt,omitempty"`
	AllowedTools       []string                    `json:"allowedTools,omitempty"`
	MaxTurns           int                         `json:"maxTurns,omitempty"`
	MaxBudgetUsd       float64                     `json:"maxBudgetUsd,omitempty"`
	PermissionMode     string                      `json:"permissionMode,omitempty"`
	Resume             string                      `json:"resume,omitempty"`
	ApprovalMode       string                      `json:"approvalMode,omitempty"`
	OutputSchema       json.RawMessage             `json:"outputSchema,omitempty"`
	MonitorURL         string                      `json:"monitorUrl,omitempty"`
	MCPServers         map[string]callerToolServer `json:"mcpServers,omitempty"`
	CallerToolUseIDKey string                      `json:"callerToolUseIDKey,omitempty"`
}
