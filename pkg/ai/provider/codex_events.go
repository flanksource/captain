package provider

import (
	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/claude"
)

// CodexCLIDefaultModel is the default model used when the caller does not
// specify one. Empty means "let `codex` pick its own default" — captain no
// longer ships a hard-coded id since the static model catalog was removed
// in favour of live `/v1/models` listings.
const CodexCLIDefaultModel = ""

func extractCodexErrorText(raw string) string {
	return history.NormalizeCodexError(raw)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// codexToolUse converts the shared normalized history shape into the stand-in
// used by the live renderer.
func codexToolUse(use history.ToolUse, fallbackModel string) claude.ToolUse {
	return claude.ToolUse{
		Tool:            use.Tool,
		Input:           use.Input,
		Timestamp:       use.Timestamp,
		CWD:             use.CWD,
		SessionID:       use.SessionID,
		ToolUseID:       use.ToolUseID,
		Source:          "codex",
		Model:           firstNonEmpty(use.Model, fallbackModel),
		ReasoningEffort: use.ReasoningEffort,
		InputTokens:     use.InputTokens + use.CacheReadTokens,
		OutputTokens:    use.OutputTokens,
		Response:        use.Response,
		AgentID:         use.AgentID,
		AgentType:       use.AgentType,
		AgentDesc:       use.AgentDesc,
	}
}

// codexResultToolUse mirrors the synthetic Result row that
// pkg/cli/ai.go's renderResultEvent constructs for Claude, preserving cost,
// usage, and error state so the "🏁 result …" footer renders consistently.
func codexResultToolUse(ev ai.Event, sessionID string) claude.ToolUse {
	input := map[string]any{}
	for k, v := range ev.Input {
		input[k] = v
	}
	if ev.CostUSD > 0 {
		if _, ok := input["total_cost_usd"]; !ok {
			input["total_cost_usd"] = ev.CostUSD
		}
	}
	tu := claude.ToolUse{
		Tool:      "Result",
		Input:     input,
		SessionID: sessionID,
		Source:    "codex",
		Model:     ev.Model,
	}
	if ev.Usage != nil {
		tu.InputTokens = ev.Usage.InputTokens
		tu.OutputTokens = ev.Usage.OutputTokens
	}
	if !ev.Success {
		tu.IsError = true
		if ev.Error != "" {
			if _, ok := tu.Input["result"]; !ok {
				tu.Input["result"] = ev.Error
			}
		}
	}
	return tu
}

// codexSessionToolUse builds the synthetic SessionInit row used by the
// shared renderer to emit a session-start banner the first time a sessionKey
// is seen. The lineRenderer treats SessionInit just like any other tool row.
func codexSessionToolUse(sessionID, model string) claude.ToolUse {
	return claude.ToolUse{
		Tool:      "SessionInit",
		SessionID: sessionID,
		Source:    "codex",
		Model:     model,
	}
}
