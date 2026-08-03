package api

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// ToolApprovalAction selects how a suspended tool call is resolved.
type ToolApprovalAction string

const (
	ToolApprovalApprove ToolApprovalAction = "approve"
	ToolApprovalDeny    ToolApprovalAction = "deny"
	ToolApprovalRespond ToolApprovalAction = "respond"
)

// ToolApprovalRequest is the serializable identity and input of a tool call
// that may require a later approval decision.
type ToolApprovalRequest struct {
	ToolCallID string          `json:"toolCallId" yaml:"toolCallId"`
	Tool       string          `json:"tool" yaml:"tool"`
	Input      json.RawMessage `json:"input,omitempty" yaml:"input,omitempty"`
}

// ToolApprovalCall records one tool request from the interrupted model turn.
// Result is set when the tool completed before a sibling call suspended the
// turn; such calls must not be executed again when the turn resumes.
type ToolApprovalCall struct {
	Request ToolApprovalRequest `json:"request" yaml:"request"`
	Result  *ToolResult         `json:"result,omitempty" yaml:"result,omitempty"`
}

// ProviderCheckpoint is opaque provider-native conversation state. It is
// persisted beside the prompt run and is deliberately excluded from every
// public transcript and session response.
type ProviderCheckpoint struct {
	Codec   string
	Version int
	Payload []byte
}

// ToolApprovalState is the durable state returned when a model turn suspends.
// Messages is the complete provider-neutral conversation ending with the
// assistant tool requests; Calls records which requests are pending or done.
type ToolApprovalState struct {
	Messages           []Message           `json:"messages" yaml:"messages"`
	Calls              []ToolApprovalCall  `json:"calls" yaml:"calls"`
	ProviderCheckpoint *ProviderCheckpoint `json:"-" yaml:"-"`
}

// ToolApprovalDecision resolves one pending call. Approve may replace Input;
// Deny may carry a Message; Respond supplies an already-computed Result.
type ToolApprovalDecision struct {
	ApprovalID string             `json:"approvalId,omitempty" yaml:"approvalId,omitempty"`
	ToolCallID string             `json:"toolCallId" yaml:"toolCallId"`
	Tool       string             `json:"tool" yaml:"tool"`
	Action     ToolApprovalAction `json:"action" yaml:"action"`
	Input      json.RawMessage    `json:"input,omitempty" yaml:"input,omitempty"`
	Message    string             `json:"message,omitempty" yaml:"message,omitempty"`
	Result     *ToolResult        `json:"result,omitempty" yaml:"result,omitempty"`
}

// ToolApprovalResume carries durable suspension state and exactly one decision
// for every pending call into a later request.
type ToolApprovalResume struct {
	State     ToolApprovalState      `json:"state" yaml:"state"`
	Decisions []ToolApprovalDecision `json:"decisions" yaml:"decisions"`
}

func (r ToolApprovalRequest) Validate() error {
	if strings.TrimSpace(r.ToolCallID) == "" {
		return fmt.Errorf("tool call ID is required")
	}
	if strings.TrimSpace(r.Tool) == "" {
		return fmt.Errorf("tool name is required for call %q", r.ToolCallID)
	}
	if len(r.Input) > 0 && !json.Valid(r.Input) {
		return fmt.Errorf("tool call %q input must be valid JSON", r.ToolCallID)
	}
	if err := validateApprovalInput(r.Input); err != nil {
		return fmt.Errorf("tool call %q input: %w", r.ToolCallID, err)
	}
	return nil
}

// Pending returns the unresolved calls in their original model-request order.
func (s ToolApprovalState) Pending() []ToolApprovalRequest {
	pending := make([]ToolApprovalRequest, 0, len(s.Calls))
	for _, call := range s.Calls {
		if call.Result == nil {
			pending = append(pending, call.Request)
		}
	}
	return pending
}

func (s ToolApprovalState) Validate() error {
	if s.ProviderCheckpoint != nil {
		if strings.TrimSpace(s.ProviderCheckpoint.Codec) == "" || s.ProviderCheckpoint.Version <= 0 || len(s.ProviderCheckpoint.Payload) == 0 {
			return fmt.Errorf("provider checkpoint requires a codec, positive version, and payload")
		}
	}
	if err := ValidateMessages(s.Messages); err != nil {
		return fmt.Errorf("approval messages: %w", err)
	}
	if len(s.Calls) == 0 {
		return fmt.Errorf("approval state must contain at least one tool call")
	}
	requests, err := approvalMessageRequests(s.Messages)
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(s.Calls))
	pending := 0
	for i, call := range s.Calls {
		if err := call.Request.Validate(); err != nil {
			return fmt.Errorf("approval call %d: %w", i+1, err)
		}
		id := call.Request.ToolCallID
		if seen[id] {
			return fmt.Errorf("duplicate approval call %q", id)
		}
		seen[id] = true
		messageRequest, ok := requests[id]
		if !ok {
			return fmt.Errorf("approval call %q is absent from the final assistant message", id)
		}
		if messageRequest.Name != call.Request.Tool {
			return fmt.Errorf("approval call %q tool %q does not match message tool %q", id, call.Request.Tool, messageRequest.Name)
		}
		if !equalJSON(messageRequest.Input, call.Request.Input) {
			return fmt.Errorf("approval call %q input does not match the final assistant message", id)
		}
		if call.Result == nil {
			pending++
			continue
		}
		if err := validateApprovalResult(id, call.Result); err != nil {
			return err
		}
	}
	if len(seen) != len(requests) {
		return fmt.Errorf("approval state must account for every tool request in the final assistant message")
	}
	if pending == 0 {
		return fmt.Errorf("approval state has no pending tool calls")
	}
	return nil
}

func (r ToolApprovalResume) Validate() error {
	if err := r.State.Validate(); err != nil {
		return err
	}
	calls := make(map[string]ToolApprovalCall, len(r.State.Calls))
	for _, call := range r.State.Calls {
		calls[call.Request.ToolCallID] = call
	}
	seen := make(map[string]bool, len(r.Decisions))
	for i, decision := range r.Decisions {
		if seen[decision.ToolCallID] {
			return fmt.Errorf("duplicate decision for tool call %q", decision.ToolCallID)
		}
		seen[decision.ToolCallID] = true
		call, ok := calls[decision.ToolCallID]
		if !ok {
			return fmt.Errorf("decision references unknown tool call %q", decision.ToolCallID)
		}
		if call.Result != nil {
			return fmt.Errorf("tool call %q is already resolved", decision.ToolCallID)
		}
		if decision.Tool != call.Request.Tool {
			return fmt.Errorf("decision tool %q does not match pending tool %q", decision.Tool, call.Request.Tool)
		}
		if err := decision.validatePayload(); err != nil {
			return fmt.Errorf("decision %d for tool call %q: %w", i+1, decision.ToolCallID, err)
		}
	}
	for _, call := range r.State.Calls {
		if call.Result == nil && !seen[call.Request.ToolCallID] {
			return fmt.Errorf("missing decision for pending tool call %q", call.Request.ToolCallID)
		}
	}
	return nil
}

func (d ToolApprovalDecision) validatePayload() error {
	switch d.Action {
	case ToolApprovalApprove:
		if d.Message != "" || d.Result != nil {
			return fmt.Errorf("approve decision can only replace tool input")
		}
		if len(d.Input) > 0 && !json.Valid(d.Input) {
			return fmt.Errorf("approve decision input must be valid JSON")
		}
		if err := validateApprovalInput(d.Input); err != nil {
			return fmt.Errorf("approve decision input: %w", err)
		}
	case ToolApprovalDeny:
		if len(d.Input) > 0 || d.Result != nil {
			return fmt.Errorf("deny decision can only carry a message")
		}
	case ToolApprovalRespond:
		if len(d.Input) > 0 {
			return fmt.Errorf("respond decision cannot replace tool input")
		}
		if d.Message != "" {
			return fmt.Errorf("respond decision cannot carry a denial message")
		}
		if d.Result == nil {
			return fmt.Errorf("respond decision requires a tool result")
		}
		if err := validateApprovalResult(d.ToolCallID, d.Result); err != nil {
			return err
		}
	default:
		return fmt.Errorf("invalid approval action %q (valid: approve, deny, respond)", d.Action)
	}
	return nil
}

func approvalMessageRequests(messages []Message) (map[string]ToolRequest, error) {
	last := messages[len(messages)-1]
	if last.Role != RoleAssistant {
		return nil, fmt.Errorf("approval messages must end with an assistant tool request")
	}
	requests := make(map[string]ToolRequest)
	for _, part := range last.Parts {
		if part.Type == PartToolRequest && part.ToolRequest != nil {
			requests[part.ToolRequest.ToolCallID] = *part.ToolRequest
		}
	}
	if len(requests) == 0 {
		return nil, fmt.Errorf("approval messages must end with an assistant tool request")
	}
	return requests, nil
}

func validateApprovalResult(callID string, result *ToolResult) error {
	if result.ToolCallID != callID {
		return fmt.Errorf("tool result call ID %q does not match approval call %q", result.ToolCallID, callID)
	}
	return (Part{Type: PartToolResult, ToolResult: result}).Validate()
}

func equalJSON(left, right json.RawMessage) bool {
	if len(left) == 0 || len(right) == 0 {
		return len(left) == len(right)
	}
	var a, b any
	return json.Unmarshal(left, &a) == nil && json.Unmarshal(right, &b) == nil && reflect.DeepEqual(a, b)
}

func validateApprovalInput(input json.RawMessage) error {
	if len(input) == 0 {
		return nil
	}
	var value map[string]any
	if err := json.Unmarshal(input, &value); err != nil {
		return fmt.Errorf("must be a JSON object")
	}
	if value == nil {
		return fmt.Errorf("must be a JSON object")
	}
	return nil
}
