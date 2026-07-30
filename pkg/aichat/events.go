package aichat

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/flanksource/captain/pkg/api"
)

type toolState struct {
	name              string
	input             map[string]any
	approvalRequested bool
}

type eventStream struct {
	writer    *SSEWriter
	blockType string
	blockID   string
	nextBlock int
	tools     map[string]toolState
	metadata  *MessageMetadata
	sessionID string
	model     string
	terminal  bool
	finished  bool
}

// EventStreamOptions carries request state needed to finish the resumed UI message.
type EventStreamOptions struct {
	ToolApproval *api.ToolApprovalResume
}

// WriteEventStream translates a Captain event channel into one complete UI Message Stream.
// Translation errors are written as an error part before the stream is closed and are
// also returned so malformed provider output cannot pass silently.
func WriteEventStream(writer *SSEWriter, events <-chan api.Event, options EventStreamOptions) error {
	if options.ToolApproval != nil {
		if err := options.ToolApproval.Validate(); err != nil {
			return fmt.Errorf("validate tool approval stream: %w", err)
		}
	}
	stream := &eventStream{writer: writer, tools: map[string]toolState{}}
	if err := stream.start(); err != nil {
		return err
	}
	if err := stream.approvalResults(options.ToolApproval); err != nil {
		return stream.fail(err)
	}
	for event := range events {
		if err := stream.event(event); err != nil {
			return stream.fail(err)
		}
	}
	if err := stream.finish(); err != nil {
		return stream.fail(err)
	}
	return nil
}

func (s *eventStream) approvalResults(resume *api.ToolApprovalResume) error {
	if resume == nil {
		return nil
	}
	for _, decision := range resume.Decisions {
		switch decision.Action {
		case api.ToolApprovalApprove:
			continue
		case api.ToolApprovalDeny:
			if err := s.writer.WritePart(Part{
				Type: "tool-output-denied", ToolCallID: decision.ToolCallID,
			}); err != nil {
				return err
			}
		case api.ToolApprovalRespond:
			part := Part{Type: "tool-output-available", ToolCallID: decision.ToolCallID}
			if decision.Result.Error != "" {
				part.Type = "tool-output-error"
				part.ErrorText = decision.Result.Error
			} else {
				var output any
				if err := json.Unmarshal(decision.Result.Output, &output); err != nil {
					return fmt.Errorf("decode tool call %q response: %w", decision.ToolCallID, err)
				}
				part.Output = output
			}
			if err := s.writer.WritePart(part); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *eventStream) start() error {
	if err := s.writer.WritePart(Part{Type: "start"}); err != nil {
		return err
	}
	return s.writer.WritePart(Part{Type: "start-step"})
}

func (s *eventStream) event(event api.Event) error {
	if s.terminal {
		return fmt.Errorf("event %q received after terminal event", event.Kind)
	}
	if event.SessionID != "" {
		s.sessionID = event.SessionID
	}
	if event.Model != "" {
		s.model = event.Model
	}
	switch event.Kind {
	case api.EventText:
		return s.delta("text", event.Text)
	case api.EventThinking:
		return s.delta("reasoning", event.Text)
	case api.EventToolUse:
		return s.toolUse(event)
	case api.EventPermission:
		return s.permission(event)
	case api.EventToolResult:
		return s.toolResult(event)
	case api.EventSystem:
		return nil
	case api.EventResult:
		return s.result(event)
	case api.EventError:
		return s.providerError(event)
	default:
		return fmt.Errorf("unsupported Captain event kind %q", event.Kind)
	}
}

func (s *eventStream) delta(kind, delta string) error {
	if delta == "" {
		return nil
	}
	if s.blockType != kind {
		if err := s.closeBlock(); err != nil {
			return err
		}
		s.blockType = kind
		s.blockID = fmt.Sprintf("%s-%d", kind, s.nextBlock)
		s.nextBlock++
		if err := s.writer.WritePart(Part{Type: kind + "-start", ID: s.blockID}); err != nil {
			return err
		}
	}
	return s.writer.WritePart(Part{Type: kind + "-delta", ID: s.blockID, Delta: delta})
}

func (s *eventStream) toolUse(event api.Event) error {
	if err := s.closeBlock(); err != nil {
		return err
	}
	if event.Tool == "" {
		return fmt.Errorf("tool use has no tool name")
	}
	if event.ToolCallID == "" {
		return fmt.Errorf("tool use %q has no tool call id", event.Tool)
	}
	if _, exists := s.tools[event.ToolCallID]; exists {
		return fmt.Errorf("duplicate tool call id %q", event.ToolCallID)
	}
	input := event.Input
	if input == nil {
		input = map[string]any{}
	}
	if err := s.writer.WritePart(Part{
		Type: "tool-input-available", ToolCallID: event.ToolCallID,
		ToolName: event.Tool, Input: input, Dynamic: true,
	}); err != nil {
		return err
	}
	s.tools[event.ToolCallID] = toolState{name: event.Tool, input: event.Input}
	return nil
}

func (s *eventStream) permission(event api.Event) error {
	if err := s.closeBlock(); err != nil {
		return err
	}
	state, err := s.correlatedTool("permission", event)
	if err != nil {
		return err
	}
	if state.approvalRequested {
		return fmt.Errorf("duplicate permission for tool call %q", event.ToolCallID)
	}
	state.approvalRequested = true
	s.tools[event.ToolCallID] = state
	return s.writer.WritePart(Part{
		Type: "tool-approval-request", ApprovalID: event.ToolCallID, ToolCallID: event.ToolCallID,
	})
}

func (s *eventStream) toolResult(event api.Event) error {
	if err := s.closeBlock(); err != nil {
		return err
	}
	if _, err := s.correlatedTool("result", event); err != nil {
		return err
	}
	output := map[string]any{"output": event.Text}
	if !event.Success {
		output["isError"] = true
	}
	if err := s.writer.WritePart(Part{Type: "tool-output-available", ToolCallID: event.ToolCallID, Output: output}); err != nil {
		return err
	}
	delete(s.tools, event.ToolCallID)
	return nil
}

func (s *eventStream) correlatedTool(kind string, event api.Event) (toolState, error) {
	if event.ToolCallID == "" {
		return toolState{}, fmt.Errorf("%s for tool %q has no tool call id", kind, event.Tool)
	}
	state, exists := s.tools[event.ToolCallID]
	if !exists {
		return toolState{}, fmt.Errorf("%s for tool call %q has no matching tool use", kind, event.ToolCallID)
	}
	if event.Tool != "" && event.Tool != state.name {
		return toolState{}, fmt.Errorf("%s for tool call %q names %q, want %q", kind, event.ToolCallID, event.Tool, state.name)
	}
	return state, nil
}

func (s *eventStream) result(event api.Event) error {
	if err := s.closeBlock(); err != nil {
		return err
	}
	if err := s.unresolvedToolError(event.ToolApproval != nil); err != nil {
		return err
	}
	if event.ToolApproval != nil {
		if err := event.ToolApproval.Validate(); err != nil {
			return fmt.Errorf("validate tool approval state: %w", err)
		}
		if err := s.validateApprovalCorrelation(event.ToolApproval); err != nil {
			return err
		}
		if err := s.writer.WritePart(Part{Type: "data-tool-approval", Data: event.ToolApproval}); err != nil {
			return err
		}
	} else {
		data := any(map[string]any{"success": event.Success})
		if len(event.StructuredData) > 0 {
			if err := json.Unmarshal(event.StructuredData, &data); err != nil {
				return fmt.Errorf("decode structured result: %w", err)
			}
		}
		if err := s.writer.WritePart(Part{Type: "data-result", Data: data}); err != nil {
			return err
		}
	}
	success := event.Success
	s.metadata = &MessageMetadata{
		ProviderSessionID: s.sessionID,
		Model:             s.model,
		Cost:              event.CostUSD,
		Success:           &success,
	}
	if event.Usage != nil {
		s.metadata.Usage = usageMetadata(*event.Usage)
		s.metadata.ContextTokens = event.Usage.InputTokens
	}
	s.terminal = true
	return nil
}

func (s *eventStream) validateApprovalCorrelation(approval *api.ToolApprovalState) error {
	seen := make(map[string]bool)
	pending := approval.Pending()
	sort.Slice(pending, func(i, j int) bool { return pending[i].ToolCallID < pending[j].ToolCallID })
	for _, request := range pending {
		state, ok := s.tools[request.ToolCallID]
		if !ok || !state.approvalRequested {
			return fmt.Errorf("approval state call %q has no matching streamed approval request", request.ToolCallID)
		}
		if request.Tool != state.name {
			return fmt.Errorf("approval state call %q names %q, want %q", request.ToolCallID, request.Tool, state.name)
		}
		if !approvalInputMatches(request.Input, state.input) {
			return fmt.Errorf("approval state call %q input does not match the streamed tool request", request.ToolCallID)
		}
		seen[request.ToolCallID] = true
	}
	streamed := make([]string, 0, len(s.tools))
	for id, state := range s.tools {
		if state.approvalRequested && !seen[id] {
			streamed = append(streamed, id)
		}
	}
	if len(streamed) > 0 {
		sort.Strings(streamed)
		return fmt.Errorf("streamed approval request %q is absent from the approval state", streamed[0])
	}
	return nil
}

func approvalInputMatches(raw json.RawMessage, input map[string]any) bool {
	if len(raw) == 0 {
		return len(input) == 0
	}
	var stateInput any
	if json.Unmarshal(raw, &stateInput) != nil {
		return false
	}
	streamedRaw, err := json.Marshal(input)
	if err != nil {
		return false
	}
	var streamedInput any
	if json.Unmarshal(streamedRaw, &streamedInput) != nil {
		return false
	}
	return reflect.DeepEqual(stateInput, streamedInput)
}

func usageMetadata(usage api.Usage) *UsageMetadata {
	return &UsageMetadata{
		InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
		ReasoningTokens: usage.ReasoningTokens, CacheReadTokens: usage.CacheReadTokens,
		CacheWriteTokens: usage.CacheWriteTokens, TotalTokens: usage.TotalTokens(),
	}
}

func (s *eventStream) providerError(event api.Event) error {
	if err := s.closeBlock(); err != nil {
		return err
	}
	if event.Error == "" {
		return fmt.Errorf("captain error event has no error message")
	}
	s.tools = map[string]toolState{}
	s.terminal = true
	return s.writer.WritePart(Part{Type: "error", ErrorText: event.Error})
}

func (s *eventStream) closeBlock() error {
	if s.blockType == "" {
		return nil
	}
	err := s.writer.WritePart(Part{Type: s.blockType + "-end", ID: s.blockID})
	s.blockType = ""
	s.blockID = ""
	return err
}

func (s *eventStream) unresolvedToolError(allowApproval bool) error {
	ids := make([]string, 0, len(s.tools))
	for id, state := range s.tools {
		if allowApproval && state.approvalRequested {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids)
	if !allowApproval {
		return fmt.Errorf("tool call %q ended without a result", ids[0])
	}
	return fmt.Errorf("tool call %q ended without a result or approval request", ids[0])
}

func (s *eventStream) finish() error {
	if s.finished {
		return fmt.Errorf("AI SDK event stream is already finished")
	}
	if err := s.closeBlock(); err != nil {
		return err
	}
	if !s.terminal {
		if err := s.unresolvedToolError(true); err != nil {
			return err
		}
	}
	if err := s.writer.WritePart(Part{Type: "finish-step"}); err != nil {
		return err
	}
	if err := s.writer.WritePart(Part{Type: "finish", MessageMetadata: s.metadata}); err != nil {
		return err
	}
	s.finished = true
	return s.writer.Done()
}

func (s *eventStream) fail(cause error) error {
	if s.finished {
		return cause
	}
	if err := s.closeBlock(); err != nil {
		return err
	}
	if err := s.writer.WritePart(Part{Type: "error", ErrorText: cause.Error()}); err != nil {
		return err
	}
	s.terminal = true
	s.tools = map[string]toolState{}
	if err := s.finish(); err != nil {
		return err
	}
	return cause
}
