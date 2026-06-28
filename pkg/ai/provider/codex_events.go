package provider

import (
	"encoding/json"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/claude"
)

// CodexCLIDefaultModel is the default model used when the caller does not
// specify one. Empty means "let `codex` pick its own default" — captain no
// longer ships a hard-coded id since the static model catalog was removed
// in favour of live `/v1/models` listings.
const CodexCLIDefaultModel = ""

// codexError is the error envelope codex emits; extractCodexErrorText unwraps a
// JSON-encoded payload nested in a `message` field into this shape.
type codexError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

// extractCodexErrorText pulls a human-readable message out of codex's error
// envelopes. Codex sometimes nests JSON-encoded payloads inside the `message`
// field (e.g. an OpenAI 400 served back as a stringified error envelope), so we
// best-effort unwrap one level when the message itself parses as JSON.
func extractCodexErrorText(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.HasPrefix(raw, "{") {
		return raw
	}
	var nested codexError
	wrapper := struct {
		Error   *codexError `json:"error"`
		Message string      `json:"message"`
	}{Error: &nested}
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil {
		return raw
	}
	if wrapper.Error != nil && wrapper.Error.Message != "" {
		return wrapper.Error.Message
	}
	if wrapper.Message != "" {
		return wrapper.Message
	}
	return raw
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// codexToolUse builds the claude.ToolUse stand-in stashed on ai.Event.Raw so the
// shared lineRenderer in pkg/cli renders codex live streams identically to
// `captain history` output. Source is hard-coded to "codex" so
// sessionKey/sessionHeaderText pick the codex icon and label.
func codexToolUse(name string, input map[string]any, sessionID, model string) claude.ToolUse {
	return claude.ToolUse{
		Tool:      name,
		Input:     input,
		SessionID: sessionID,
		Source:    "codex",
		Model:     model,
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
