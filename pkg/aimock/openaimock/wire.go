// ABOUTME: Decoding of inbound Responses and Chat Completions requests into the protocol-neutral aimock.Request.
// ABOUTME: Both shapes collapse onto the same normalized turns, so one matcher works across either endpoint.

package openaimock

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/flanksource/captain/pkg/aimock"
)

// responsesRequest is the subset of a /v1/responses body worth matching on.
type responsesRequest struct {
	Model        string          `json:"model"`
	Instructions string          `json:"instructions,omitempty"`
	Input        json.RawMessage `json:"input"`
	Stream       bool            `json:"stream,omitempty"`
}

// inputItem covers every entry shape the Responses API accepts in `input`:
// messages carry content, function_call names a tool, function_call_output
// returns its result under the same call_id.
type inputItem struct {
	Type      string          `json:"type,omitempty"`
	Role      string          `json:"role,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	Name      string          `json:"name,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
	Output    json.RawMessage `json:"output,omitempty"`
}

// contentPart is one part of a message's content array, in either direction:
// input_text on the way in, output_text on the way back.
type contentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// decodeResponses parses a /v1/responses body and normalizes it for matching.
func decodeResponses(r *http.Request, body []byte) (responsesRequest, aimock.Request, error) {
	var wire responsesRequest
	if err := json.Unmarshal(body, &wire); err != nil {
		return wire, aimock.Request{}, fmt.Errorf("decode responses request: %w", err)
	}

	norm := aimock.Request{
		Model:   wire.Model,
		System:  wire.Instructions,
		Stream:  wire.Stream,
		Headers: headerMap(r),
	}

	// A bare string input is the single-user-turn shorthand.
	var text string
	if json.Unmarshal(wire.Input, &text) == nil {
		norm.Messages = []aimock.Message{{Role: aimock.RoleUser, Content: text}}
		return wire, norm, nil
	}

	var items []inputItem
	if err := json.Unmarshal(wire.Input, &items); err != nil {
		return wire, norm, fmt.Errorf("decode responses input: %w", err)
	}

	// call_id → tool name, so a later function_call_output — which carries only
	// the id — resolves to the name a scenario matches on.
	callNames := map[string]string{}
	for _, in := range items {
		switch in.Type {
		case "function_call":
			if in.CallID != "" && in.Name != "" {
				callNames[in.CallID] = in.Name
			}
			norm.Messages = append(norm.Messages, aimock.Message{Role: aimock.RoleAssistant, Content: in.Arguments})
		case "function_call_output":
			// Tool output is the user side of an agent loop: normalizing it to a
			// user turn is what makes tool_result_for work the same way here as
			// it does for the Anthropic protocol.
			norm.Messages = append(norm.Messages, aimock.Message{
				Role:        aimock.RoleUser,
				Content:     flattenContent(in.Output),
				ToolResults: []string{toolName(callNames, in.CallID)},
			})
		case "reasoning":
			// Reasoning items are echoed back for context; they carry no prose to
			// match on and no turn of their own.
		default:
			norm.Messages = append(norm.Messages, aimock.Message{
				Role:    normalizeRole(in.Role),
				Content: flattenContent(in.Content),
			})
		}
	}
	return wire, norm, nil
}

// chatRequest is the subset of a /v1/chat/completions body worth matching on.
type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream,omitempty"`
}

type chatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	ToolCalls  []chatToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// decodeChat parses a /v1/chat/completions body and normalizes it for matching.
func decodeChat(r *http.Request, body []byte) (chatRequest, aimock.Request, error) {
	var wire chatRequest
	if err := json.Unmarshal(body, &wire); err != nil {
		return wire, aimock.Request{}, fmt.Errorf("decode chat request: %w", err)
	}

	norm := aimock.Request{Model: wire.Model, Stream: wire.Stream, Headers: headerMap(r)}

	callNames := map[string]string{}
	var systems []string
	for _, msg := range wire.Messages {
		role := normalizeRole(msg.Role)
		content := flattenContent(msg.Content)

		for _, call := range msg.ToolCalls {
			if call.ID != "" && call.Function.Name != "" {
				callNames[call.ID] = call.Function.Name
			}
		}

		switch {
		case role == aimock.RoleSystem:
			systems = append(systems, content)
		case msg.Role == "tool":
			// Same reasoning as function_call_output above: a tool reply is the
			// user side of the loop.
			norm.Messages = append(norm.Messages, aimock.Message{
				Role:        aimock.RoleUser,
				Content:     content,
				ToolResults: []string{toolName(callNames, msg.ToolCallID)},
			})
		default:
			norm.Messages = append(norm.Messages, aimock.Message{Role: role, Content: content})
		}
	}
	norm.System = strings.Join(systems, "\n")
	return wire, norm, nil
}

// normalizeRole collapses the wire's role vocabulary onto aimock's. `developer`
// is the Responses API's rename of `system`.
func normalizeRole(role string) string {
	switch role {
	case "system", "developer":
		return aimock.RoleSystem
	case "assistant":
		return aimock.RoleAssistant
	default:
		return aimock.RoleUser
	}
}

// toolName resolves a call id back to the tool it invoked, falling back to the
// id itself so an unpaired result is still matchable rather than silently blank.
func toolName(callNames map[string]string, callID string) string {
	if name, ok := callNames[callID]; ok {
		return name
	}
	return callID
}

// flattenContent accepts the bare-string and part-array forms of a content
// field, in either direction.
func flattenContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []contentPart
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var out []string
	for _, part := range parts {
		if part.Text != "" {
			out = append(out, part.Text)
		}
	}
	return strings.Join(out, "\n")
}

func headerMap(r *http.Request) map[string]string {
	out := make(map[string]string, len(r.Header))
	for key := range r.Header {
		out[strings.ToLower(key)] = r.Header.Get(key)
	}
	return out
}
