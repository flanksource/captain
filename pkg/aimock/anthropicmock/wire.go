// ABOUTME: Decoding of inbound Messages API requests into the protocol-neutral aimock.Request.
// ABOUTME: Handles both the string and block-array forms of `system` and message content.

package anthropicmock

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/flanksource/captain/pkg/aimock"
)

// messagesRequest is the subset of the Messages API request body worth matching on.
type messagesRequest struct {
	Model     string          `json:"model"`
	System    json.RawMessage `json:"system,omitempty"`
	Messages  []wireMessage   `json:"messages"`
	Stream    bool            `json:"stream,omitempty"`
	MaxTokens int             `json:"max_tokens,omitempty"`
}

type wireMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// inputBlock covers every content-block shape we care about across both
// directions: text/thinking carry prose, tool_result names the tool call it
// answers, tool_use names the tool.
type inputBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Name      string          `json:"name,omitempty"`
	ID        string          `json:"id,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
}

// decodeRequest parses the body and normalizes it for matching.
func decodeRequest(r *http.Request, body []byte) (messagesRequest, aimock.Request, error) {
	var wire messagesRequest
	if err := json.Unmarshal(body, &wire); err != nil {
		return wire, aimock.Request{}, fmt.Errorf("decode messages request: %w", err)
	}

	norm := aimock.Request{
		Model:   wire.Model,
		System:  flattenSystem(wire.System),
		Stream:  wire.Stream,
		Headers: headerMap(r),
	}

	// tool_use ids seen so far, so a later tool_result can be resolved back to
	// the tool name the scenario matches on.
	toolNames := map[string]string{}
	for _, msg := range wire.Messages {
		content, results := flattenContent(msg.Content, toolNames)
		norm.Messages = append(norm.Messages, aimock.Message{Role: msg.Role, Content: content, ToolResults: results})
	}
	return wire, norm, nil
}

// flattenSystem accepts both the string form and the block-array form of the
// top-level `system` field. Claude Code sends the array form.
func flattenSystem(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []inputBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, block := range blocks {
		if block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// flattenContent reduces a message's content to its prose plus the names of any
// tools whose results it carries. toolNames accumulates tool_use id → name
// across the conversation so tool_result blocks, which only carry the id, can
// be resolved to a name.
func flattenContent(raw json.RawMessage, toolNames map[string]string) (string, []string) {
	if len(raw) == 0 {
		return "", nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}

	var blocks []inputBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return "", nil
	}

	var parts, results []string
	for _, block := range blocks {
		switch block.Type {
		case "text":
			parts = append(parts, block.Text)
		case "thinking":
			parts = append(parts, block.Thinking)
		case "tool_use":
			if block.ID != "" && block.Name != "" {
				toolNames[block.ID] = block.Name
			}
		case "tool_result":
			if name, ok := toolNames[block.ToolUseID]; ok {
				results = append(results, name)
			} else if block.ToolUseID != "" {
				results = append(results, block.ToolUseID)
			}
			if inner := flattenToolResult(block.Content); inner != "" {
				parts = append(parts, inner)
			}
		}
	}
	return strings.Join(parts, "\n"), results
}

// flattenToolResult extracts the text of a tool_result payload, which may be a
// bare string or a nested block array.
func flattenToolResult(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []inputBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, block := range blocks {
		if block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func headerMap(r *http.Request) map[string]string {
	out := make(map[string]string, len(r.Header))
	for key := range r.Header {
		out[strings.ToLower(key)] = r.Header.Get(key)
	}
	return out
}
