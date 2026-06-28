package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/claude"
)

// TestRenderEvent_CodexLiveUsesLineRenderer verifies that codex live events
// flow through the same shared lineRenderer as `captain history` does for
// codex JSONL — emitting a session-start banner, a tool row, and a result row
// with cost/usage. Without unification, renderEvent falls back to the bare
// "[tool] name" / "[result] ..." printer.
func TestRenderEvent_CodexLiveUsesLineRenderer(t *testing.T) {
	var buf bytes.Buffer
	renderer := newLineRenderer(&buf, 8)

	session := claude.ToolUse{
		Tool:      "SessionInit",
		SessionID: "019e0365-dc2a-7ad0-a5a8-78936481a928",
		Source:    "codex",
		Model:     "gpt-5",
	}
	exec := claude.ToolUse{
		Tool:      "Bash",
		Input:     map[string]any{"command": "ls"},
		SessionID: "019e0365-dc2a-7ad0-a5a8-78936481a928",
		Source:    "codex",
		Model:     "gpt-5",
	}
	result := claude.ToolUse{
		Tool:         "Result",
		Input:        map[string]any{"total_cost_usd": 0.5},
		SessionID:    "019e0365-dc2a-7ad0-a5a8-78936481a928",
		Source:       "codex",
		Model:        "gpt-5",
		InputTokens:  100,
		OutputTokens: 50,
	}

	renderEvent(nil, renderer, ai.Event{
		Kind:      ai.EventSystem,
		Tool:      "SessionInit",
		SessionID: session.SessionID,
		Model:     session.Model,
		Raw:       session,
	})
	renderEvent(nil, renderer, ai.Event{
		Kind:  ai.EventToolUse,
		Tool:  exec.Tool,
		Input: exec.Input,
		Model: exec.Model,
		Raw:   exec,
	})
	renderEvent(nil, renderer, ai.Event{
		Kind:    ai.EventResult,
		Tool:    "Result",
		Model:   result.Model,
		Success: true,
		CostUSD: 0.5,
		Usage:   &ai.Usage{InputTokens: 100, OutputTokens: 50},
		Raw:     result,
	})

	out := buf.String()
	for _, want := range []string{
		"Codex",    // session header capitalises the source name
		"gpt-5",    // model in the header
		"019e0365", // shortened session id
		"Bash",     // tool row label
		"$0.5",     // cost
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered output missing %q\nfull output:\n%s", want, out)
		}
	}

	// Negative: the bare fallback printer must NOT be used when Raw is set.
	for _, forbidden := range []string{"[tool]", "[result]", "[session]"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("output should not contain bare fallback %q\nfull output:\n%s", forbidden, out)
		}
	}
}
