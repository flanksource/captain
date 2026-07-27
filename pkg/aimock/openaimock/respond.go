// ABOUTME: The `respond:` schema for the openai scenario section, and the output items it renders into.
// ABOUTME: Shaped for the OpenAI wire APIs — reasoning, function_call and message items with a finish reason.

package openaimock

import (
	"encoding/json"
	"fmt"
)

// Finish reasons the Chat Completions API returns. The Responses API expresses
// the same thing as a status, which resolvedStatus derives from these.
const (
	FinishStop      = "stop"
	FinishToolCalls = "tool_calls"
	FinishLength    = "length"
)

// Respond is one scripted assistant reply. Items are emitted in the order
// reasoning → function_call → message, matching how the Responses API orders its
// output array.
type Respond struct {
	Reasoning    string        `json:"reasoning,omitempty" yaml:"reasoning,omitempty"`
	Text         string        `json:"text,omitempty" yaml:"text,omitempty"`
	FunctionCall *FunctionCall `json:"function_call,omitempty" yaml:"function_call,omitempty"`

	FinishReason string `json:"finish_reason,omitempty" yaml:"finish_reason,omitempty"`
	Usage        Usage  `json:"usage,omitempty" yaml:"usage,omitempty"`

	// Error, when set, makes this rule return an API error instead of a reply —
	// for exercising the retry and error-mapping paths.
	Error *Error `json:"error,omitempty" yaml:"error,omitempty"`
}

// FunctionCall is a scripted tool call. Arguments are written as YAML and
// marshalled to the JSON string the wire carries.
type FunctionCall struct {
	Name      string         `json:"name" yaml:"name"`
	CallID    string         `json:"call_id,omitempty" yaml:"call_id,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty" yaml:"arguments,omitempty"`
}

// Usage is the token accounting attached to a reply.
type Usage struct {
	Input     int `json:"input,omitempty" yaml:"input,omitempty"`
	Output    int `json:"output,omitempty" yaml:"output,omitempty"`
	CacheRead int `json:"cache_read,omitempty" yaml:"cache_read,omitempty"`
	Reasoning int `json:"reasoning,omitempty" yaml:"reasoning,omitempty"`
}

// Error is a scripted API error.
type Error struct {
	Status  int    `json:"status,omitempty" yaml:"status,omitempty"`
	Type    string `json:"type,omitempty" yaml:"type,omitempty"`
	Code    string `json:"code,omitempty" yaml:"code,omitempty"`
	Message string `json:"message,omitempty" yaml:"message,omitempty"`
}

// resolvedFinishReason infers the finish reason when the scenario omits it: a
// reply carrying a tool call stops for tool_calls, anything else stops normally.
func (r Respond) resolvedFinishReason() string {
	if r.FinishReason != "" {
		return r.FinishReason
	}
	if r.FunctionCall != nil {
		return FinishToolCalls
	}
	return FinishStop
}

// item is one entry of the Responses API output array. It renders to a map
// rather than a struct so each item type carries exactly its own field set on
// the wire — a reasoning item has "summary": [], a message item has
// "content": [], and neither carries the other's field even as null.
type item struct {
	Type      string
	ID        string
	Text      string
	Name      string
	CallID    string
	Arguments string
}

// items renders the reply into ordered output items. A reply with neither text
// nor a tool call still produces an empty message, because every Responses reply
// has at least one output item.
func (r Respond) items() []item {
	var out []item
	if r.Reasoning != "" {
		out = append(out, item{Type: "reasoning", ID: fmt.Sprintf("rs_mock_%d", len(out)), Text: r.Reasoning})
	}
	if r.FunctionCall != nil {
		callID := r.FunctionCall.CallID
		if callID == "" {
			callID = fmt.Sprintf("call_mock_%d", len(out))
		}
		arguments := r.FunctionCall.Arguments
		if arguments == nil {
			arguments = map[string]any{}
		}
		raw, err := json.Marshal(arguments)
		if err != nil {
			// Arguments came from YAML the scenario author wrote; a shape that
			// cannot be marshalled is an authoring bug worth showing on the wire
			// rather than swallowing into empty arguments.
			raw = []byte(fmt.Sprintf("{%q:%q}", "aimock_error", err.Error()))
		}
		out = append(out, item{
			Type:      "function_call",
			ID:        fmt.Sprintf("fc_mock_%d", len(out)),
			Name:      r.FunctionCall.Name,
			CallID:    callID,
			Arguments: string(raw),
		})
	}
	if r.Text != "" || len(out) == 0 {
		out = append(out, item{Type: "message", ID: fmt.Sprintf("msg_mock_%d", len(out)), Text: r.Text})
	}
	return out
}

// added is the shell announced by response.output_item.added: identity only, with
// the streamed payload still empty.
func (i item) added() map[string]any {
	switch i.Type {
	case "reasoning":
		return map[string]any{"id": i.ID, "type": "reasoning", "summary": []any{}}
	case "function_call":
		return map[string]any{"id": i.ID, "type": "function_call", "status": "in_progress", "name": i.Name, "call_id": i.CallID, "arguments": ""}
	default:
		return map[string]any{"id": i.ID, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}
	}
}

// done is the completed item, carried by both response.output_item.done and the
// output array of response.completed.
func (i item) done() map[string]any {
	switch i.Type {
	case "reasoning":
		return map[string]any{
			"id": i.ID, "type": "reasoning",
			"summary": []any{map[string]any{"type": "summary_text", "text": i.Text}},
		}
	case "function_call":
		return map[string]any{"id": i.ID, "type": "function_call", "status": "completed", "name": i.Name, "call_id": i.CallID, "arguments": i.Arguments}
	default:
		return map[string]any{
			"id": i.ID, "type": "message", "status": "completed", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": i.Text, "annotations": []any{}}},
		}
	}
}

// responsesUsage is the usage object the Responses API reports.
type responsesUsage struct {
	InputTokens         int          `json:"input_tokens"`
	InputTokensDetails  inputDetails `json:"input_tokens_details"`
	OutputTokens        int          `json:"output_tokens"`
	OutputTokensDetails outputDetail `json:"output_tokens_details"`
	TotalTokens         int          `json:"total_tokens"`
}

type inputDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type outputDetail struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

func (u Usage) responses() responsesUsage {
	return responsesUsage{
		InputTokens:         u.Input,
		InputTokensDetails:  inputDetails{CachedTokens: u.CacheRead},
		OutputTokens:        u.Output,
		OutputTokensDetails: outputDetail{ReasoningTokens: u.Reasoning},
		TotalTokens:         u.Input + u.Output,
	}
}

// chatUsage is the same accounting under the Chat Completions API's older names.
type chatUsage struct {
	PromptTokens            int          `json:"prompt_tokens"`
	CompletionTokens        int          `json:"completion_tokens"`
	TotalTokens             int          `json:"total_tokens"`
	PromptTokensDetails     inputDetails `json:"prompt_tokens_details"`
	CompletionTokensDetails outputDetail `json:"completion_tokens_details"`
}

func (u Usage) chat() chatUsage {
	return chatUsage{
		PromptTokens:            u.Input,
		CompletionTokens:        u.Output,
		TotalTokens:             u.Input + u.Output,
		PromptTokensDetails:     inputDetails{CachedTokens: u.CacheRead},
		CompletionTokensDetails: outputDetail{ReasoningTokens: u.Reasoning},
	}
}

// errorResponse is the OpenAI error envelope, shared by both APIs.
type errorResponse struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    *string `json:"code"`
}

func newErrorResponse(errType, code, message string) errorResponse {
	resp := errorResponse{Error: errorBody{Message: message, Type: errType}}
	if code != "" {
		resp.Error.Code = &code
	}
	return resp
}

// fallbackRespond is the bland reply served when Lenient is set and no rule
// matched. Strict mode (the default) returns aimock.MissStatus instead.
func fallbackRespond() *Respond {
	return &Respond{
		Text:         "captain-mock: unmatched request",
		FinishReason: FinishStop,
		Usage:        Usage{Input: 1, Output: 1},
	}
}
