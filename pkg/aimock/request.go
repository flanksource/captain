// ABOUTME: Protocol-neutral request shape that both mock servers normalize their wire body into.
// ABOUTME: Matching happens against this, so matchers are written once and work for either protocol.

package aimock

import "strings"

// Role values on a normalized Message. Both wire protocols collapse onto these.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Message is one normalized conversation turn. Content is the flattened text of
// every text-ish block in the turn; ToolResults names the tools whose results
// this turn carries back to the model.
type Message struct {
	Role        string   `json:"role"`
	Content     string   `json:"content,omitempty"`
	ToolResults []string `json:"toolResults,omitempty"`
}

// Request is what a matcher sees: the wire body of either protocol reduced to
// the fields worth matching on. Each server builds one of these from its own
// request type before consulting the rules.
type Request struct {
	Model    string            `json:"model,omitempty"`
	System   string            `json:"system,omitempty"`
	Messages []Message         `json:"messages,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Stream   bool              `json:"stream,omitempty"`
}

// LastUserText is the content of the most recent user turn — the "prompt" that
// prompt_contains and prompt_regex match against.
func (r Request) LastUserText() string {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == RoleUser {
			return r.Messages[i].Content
		}
	}
	return ""
}

// AllText concatenates every turn's content, for matchers that should see the
// whole conversation rather than only the latest turn.
func (r Request) AllText() string {
	parts := make([]string, 0, len(r.Messages))
	for _, m := range r.Messages {
		if m.Content != "" {
			parts = append(parts, m.Content)
		}
	}
	return strings.Join(parts, "\n")
}

// ToolResultNames lists the tools whose results appear in the most recent user
// turn. An agent loop's second request carries the first turn's tool results
// here, which is what tool_result_for keys off.
func (r Request) ToolResultNames() []string {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == RoleUser && len(r.Messages[i].ToolResults) > 0 {
			return r.Messages[i].ToolResults
		}
	}
	return nil
}
