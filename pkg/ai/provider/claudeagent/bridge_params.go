package claudeagent

import (
	"encoding/json"
	"fmt"

	"github.com/flanksource/captain/pkg/api"
)

// bridgeProtocolVersion is the agent.ts protocol this provider speaks. It must
// equal PROTOCOL_VERSION in protocol.ts; bump both when the bridge contract
// changes. Version 2 added can_use_tool interrupt and the elicit request.
const bridgeProtocolVersion = 2

// initializeReply is agent.ts's answer to initialize.
type initializeReply struct {
	OK              bool `json:"ok"`
	ProtocolVersion int  `json:"protocolVersion"`
}

// checkBridgeProtocol refuses a bridge older than this provider, such as a
// stale agent.ts left running across an upgrade, which would silently drop
// the fields and requests it does not know.
func checkBridgeProtocol(raw json.RawMessage) error {
	var reply initializeReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return fmt.Errorf("claude-agent: unreadable initialize reply %s: %w", raw, err)
	}
	if reply.ProtocolVersion < bridgeProtocolVersion {
		return fmt.Errorf("claude-agent: the agent.ts bridge speaks protocol %d but captain needs bridge protocol %d; restart captain so it reinstalls the embedded bridge", reply.ProtocolVersion, bridgeProtocolVersion)
	}
	return nil
}

type initializeParams struct {
	Cwd                string     `json:"cwd,omitempty"`
	Model              string     `json:"model,omitempty"`
	Effort             api.Effort `json:"effort,omitempty"`
	SystemPrompt       string     `json:"systemPrompt,omitempty"`
	AppendSystemPrompt string     `json:"appendSystemPrompt,omitempty"`
	AllowedTools       []string   `json:"allowedTools,omitempty"`
	DisallowedTools    []string   `json:"disallowedTools,omitempty"`
	// AdditionalDirs are paths outside Cwd the SDK's own tools may reach. Without
	// them a run in a worktree raises a permission request for every read of its
	// parent checkout, and a headless run has nobody to answer one.
	AdditionalDirs     []string                    `json:"additionalDirectories,omitempty"`
	MaxTurns           int                         `json:"maxTurns,omitempty"`
	MaxBudgetUsd       float64                     `json:"maxBudgetUsd,omitempty"`
	PermissionMode     string                      `json:"permissionMode,omitempty"`
	Sandbox            map[string]any              `json:"sandbox,omitempty"`
	Resume             string                      `json:"resume,omitempty"`
	ApprovalMode       string                      `json:"approvalMode,omitempty"`
	OutputSchema       json.RawMessage             `json:"outputSchema,omitempty"`
	MonitorURL         string                      `json:"monitorUrl,omitempty"`
	MCPServers         map[string]callerToolServer `json:"mcpServers,omitempty"`
	CallerToolUseIDKey string                      `json:"callerToolUseIDKey,omitempty"`
	// StrictMCPConfig limits the SDK to MCPServers. Without it every ambient
	// server (.mcp.json, user settings, plugins) loads alongside them.
	StrictMCPConfig bool `json:"strictMcpConfig,omitempty"`
}
