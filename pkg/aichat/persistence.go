package aichat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api"
)

type assistantMessageBuilder struct {
	message   UIMessage
	toolParts map[string]int
	sessionID string
	model     string
	replace   bool
}

type assistantMessageBuilderOptions struct {
	ChatID string
	Seed   *UIMessage
	Resume *api.ToolApprovalResume
}

func newAssistantMessageBuilder(options assistantMessageBuilderOptions) (*assistantMessageBuilder, error) {
	id := ""
	if options.ChatID != "" {
		id = options.ChatID + "-assistant"
	}
	builder := &assistantMessageBuilder{
		message:   UIMessage{ID: id, Role: string(api.RoleAssistant), Parts: []UIPart{}},
		toolParts: map[string]int{},
		replace:   options.Resume != nil,
	}
	if options.Resume == nil {
		return builder, nil
	}
	if options.Seed == nil {
		return nil, fmt.Errorf("approval resume has no suspended assistant message")
	}
	if !strings.EqualFold(options.Seed.Role, string(api.RoleAssistant)) {
		return nil, fmt.Errorf("approval resume seed must have assistant role")
	}
	builder.message = *options.Seed
	builder.message.Parts = append([]UIPart(nil), options.Seed.Parts...)
	for i, part := range builder.message.Parts {
		if !part.IsTool() {
			continue
		}
		if part.ToolCallID == "" {
			return nil, fmt.Errorf("persisted tool part has no call ID")
		}
		if _, exists := builder.toolParts[part.ToolCallID]; exists {
			return nil, fmt.Errorf("persisted duplicate tool call %q", part.ToolCallID)
		}
		builder.toolParts[part.ToolCallID] = i
	}
	pending := make(map[string]api.ToolApprovalRequest, len(options.Resume.Decisions))
	for _, request := range options.Resume.State.Pending() {
		pending[request.ToolCallID] = request
	}
	for _, decision := range options.Resume.Decisions {
		request := pending[decision.ToolCallID]
		part, err := builder.toolPart(decision.ToolCallID, request.Tool)
		if err != nil {
			return nil, err
		}
		if !equalPartJSON(part.Input, request.Input) {
			return nil, fmt.Errorf("persisted tool call %q input does not match approval state", decision.ToolCallID)
		}
		if err := applyApprovalDecision(part, decision); err != nil {
			return nil, err
		}
	}
	return builder, nil
}

func (s *Service) persistedEvents(ctx context.Context, request ChatRequest, source <-chan api.Event) <-chan api.Event {
	if request.ThreadID == "" {
		return source
	}
	out := make(chan api.Event)
	go func() {
		defer close(out)
		seed, err := s.approvalPersistenceSeed(ctx, request)
		if err != nil {
			sendEvent(ctx, out, api.Event{Kind: api.EventError, Error: err.Error()})
			return
		}
		builder, err := newAssistantMessageBuilder(assistantMessageBuilderOptions{
			ChatID: request.ID, Seed: seed, Resume: request.ToolApproval,
		})
		if err != nil {
			sendEvent(ctx, out, api.Event{Kind: api.EventError, Error: err.Error()})
			return
		}
		persisted := false
		for event := range source {
			if err := builder.apply(event); err != nil {
				sendEvent(ctx, out, api.Event{Kind: api.EventError, Error: err.Error(), Model: event.Model})
				return
			}
			if !persisted && event.Kind != api.EventResult && event.Kind != api.EventError && event.SessionID != "" {
				if err := s.persistEvent(ctx, request.ThreadID, event); err != nil {
					sendEvent(ctx, out, api.Event{Kind: api.EventError, Error: err.Error(), Model: event.Model})
					return
				}
			}
			if !persisted && (event.Kind == api.EventResult || event.Kind == api.EventError) {
				if err := s.persistCompletedTurn(ctx, request.ThreadID, builder, event); err != nil {
					sendEvent(ctx, out, api.Event{Kind: api.EventError, Error: err.Error(), Model: event.Model})
					return
				}
				persisted = true
			}
			if !sendEvent(ctx, out, event) {
				return
			}
		}
		if !persisted && len(builder.message.Parts) > 0 {
			if err := s.persistAssistantMessage(ctx, request.ThreadID, builder); err != nil {
				sendEvent(ctx, out, api.Event{Kind: api.EventError, Error: fmt.Sprintf("persist assistant message: %v", err)})
			}
		}
	}()
	return out
}

func (s *Service) approvalPersistenceSeed(ctx context.Context, request ChatRequest) (*UIMessage, error) {
	if request.ToolApproval == nil {
		return nil, nil
	}
	if len(request.Messages) > 0 {
		last := request.Messages[len(request.Messages)-1]
		if strings.EqualFold(last.Role, string(api.RoleAssistant)) {
			return &last, nil
		}
	}
	thread, err := s.options.Threads.Get(ctx, request.ThreadID)
	if err != nil {
		return nil, fmt.Errorf("load suspended assistant message: %w", err)
	}
	if len(thread.Messages) == 0 {
		return nil, fmt.Errorf("thread %q has no suspended assistant message", request.ThreadID)
	}
	last := thread.Messages[len(thread.Messages)-1]
	return &last, nil
}

func sendEvent(ctx context.Context, target chan<- api.Event, event api.Event) bool {
	select {
	case target <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Service) persistCompletedTurn(ctx context.Context, threadID string, builder *assistantMessageBuilder, event api.Event) error {
	if err := s.persistEvent(ctx, threadID, event); err != nil {
		return err
	}
	if len(builder.message.Parts) == 0 {
		return fmt.Errorf("completed assistant turn has no message parts")
	}
	if err := s.persistAssistantMessage(ctx, threadID, builder); err != nil {
		return fmt.Errorf("persist assistant message: %w", err)
	}
	return nil
}

func (s *Service) persistAssistantMessage(ctx context.Context, threadID string, builder *assistantMessageBuilder) error {
	if builder.replace {
		return s.options.Threads.ReplaceLastMessage(ctx, threadID, builder.message)
	}
	return s.options.Threads.AppendMessage(ctx, threadID, builder.message)
}

func (b *assistantMessageBuilder) apply(event api.Event) error {
	if event.SessionID != "" {
		b.sessionID = event.SessionID
	}
	if event.Model != "" {
		b.model = event.Model
	}
	switch event.Kind {
	case api.EventText:
		b.appendText("text", event.Text)
	case api.EventThinking:
		b.appendText("reasoning", event.Text)
	case api.EventToolUse:
		return b.toolUse(event)
	case api.EventPermission:
		return b.permission(event)
	case api.EventToolResult:
		return b.toolResult(event)
	case api.EventResult:
		return b.result(event)
	case api.EventError:
		payload, err := json.Marshal(map[string]string{"error": event.Error})
		if err != nil {
			return err
		}
		b.message.Parts = append(b.message.Parts, UIPart{Type: "data-error", Data: payload})
	case api.EventSystem:
	}
	return nil
}

func (b *assistantMessageBuilder) appendText(partType, text string) {
	if text == "" {
		return
	}
	if len(b.message.Parts) > 0 && b.message.Parts[len(b.message.Parts)-1].Type == partType {
		b.message.Parts[len(b.message.Parts)-1].Text += text
		return
	}
	b.message.Parts = append(b.message.Parts, UIPart{Type: partType, Text: text})
}

func (b *assistantMessageBuilder) toolUse(event api.Event) error {
	if event.ToolCallID == "" || event.Tool == "" {
		return fmt.Errorf("persist tool use requires a tool name and call ID")
	}
	input, err := json.Marshal(event.Input)
	if err != nil {
		return fmt.Errorf("marshal tool %q input: %w", event.Tool, err)
	}
	if _, exists := b.toolParts[event.ToolCallID]; exists {
		part, err := b.toolPart(event.ToolCallID, event.Tool)
		if err != nil {
			return err
		}
		if part.State != "approval-responded" ||
			part.Approval == nil ||
			part.Approval.Approved == nil ||
			!*part.Approval.Approved {
			return fmt.Errorf("persist duplicate tool call %q", event.ToolCallID)
		}
		if !approvalInputMatches(part.Input, event.Input) {
			return fmt.Errorf("persist resumed tool call %q input does not match approval", event.ToolCallID)
		}
		part.State = "input-available"
		part.Input = input
		part.Output = nil
		part.ErrorText = ""
		return nil
	}
	b.toolParts[event.ToolCallID] = len(b.message.Parts)
	b.message.Parts = append(b.message.Parts, UIPart{
		Type: "dynamic-tool", ToolName: event.Tool, ToolCallID: event.ToolCallID,
		State: "input-available", Input: input,
	})
	return nil
}

func (b *assistantMessageBuilder) permission(event api.Event) error {
	part, err := b.toolPart(event.ToolCallID, event.Tool)
	if err != nil {
		return err
	}
	part.State = "approval-requested"
	part.Approval = &Approval{ID: event.ToolCallID}
	return nil
}

func (b *assistantMessageBuilder) toolResult(event api.Event) error {
	part, err := b.toolPart(event.ToolCallID, event.Tool)
	if err != nil {
		return err
	}
	if event.Success {
		part.State = "output-available"
		part.Output, err = jsonValue(event.Text)
		if err != nil {
			return fmt.Errorf("marshal tool %q output: %w", event.Tool, err)
		}
		return nil
	}
	part.State = "output-error"
	part.ErrorText = event.Text
	return nil
}

func (b *assistantMessageBuilder) toolPart(callID, name string) (*UIPart, error) {
	index, ok := b.toolParts[callID]
	if !ok {
		return nil, fmt.Errorf("persist tool event %q has no matching tool use", callID)
	}
	part := &b.message.Parts[index]
	if name != "" && part.ToolName != name {
		return nil, fmt.Errorf("persist tool event %q names %q, want %q", callID, name, part.ToolName)
	}
	return part, nil
}

func (b *assistantMessageBuilder) result(event api.Event) error {
	if err := b.validateToolStates(event.ToolApproval != nil); err != nil {
		return err
	}
	dataType := "data-result"
	data := event.StructuredData
	if event.ToolApproval != nil {
		dataType = "data-tool-approval"
		var err error
		data, err = json.Marshal(event.ToolApproval)
		if err != nil {
			return fmt.Errorf("marshal tool approval state: %w", err)
		}
	} else if len(data) == 0 {
		var err error
		data, err = json.Marshal(map[string]bool{"success": event.Success})
		if err != nil {
			return fmt.Errorf("marshal result state: %w", err)
		}
	}
	b.message.Parts = append(b.message.Parts, UIPart{Type: dataType, Data: data})
	success := event.Success
	b.message.Metadata = &MessageMetadata{
		ProviderSessionID: b.sessionID, Model: b.model, Cost: event.CostUSD, Success: &success,
	}
	if event.Usage != nil {
		b.message.Metadata.Usage = usageMetadata(*event.Usage)
		b.message.Metadata.ContextTokens = event.Usage.InputTokens
	}
	return nil
}

func (b *assistantMessageBuilder) validateToolStates(allowApproval bool) error {
	for _, part := range b.message.Parts {
		if !part.IsTool() {
			continue
		}
		switch part.State {
		case "output-available", "output-error", "output-denied":
		case "approval-requested":
			if allowApproval {
				continue
			}
			return fmt.Errorf("persist tool call %q ended without an approval result", part.ToolCallID)
		default:
			return fmt.Errorf("persist tool call %q ended in non-terminal state %q", part.ToolCallID, part.State)
		}
	}
	return nil
}

func applyApprovalDecision(part *UIPart, decision api.ToolApprovalDecision) error {
	switch decision.Action {
	case api.ToolApprovalApprove:
		approved := true
		part.State = "approval-responded"
		part.Approval = &Approval{ID: decision.ToolCallID, Approved: &approved}
	case api.ToolApprovalDeny:
		approved := false
		part.State = "output-denied"
		part.Approval = &Approval{
			ID: decision.ToolCallID, Approved: &approved, Reason: decision.Message,
		}
	case api.ToolApprovalRespond:
		if decision.Result == nil {
			return fmt.Errorf("tool call %q response has no result", decision.ToolCallID)
		}
		if decision.Result.Error != "" {
			part.State = "output-error"
			part.ErrorText = decision.Result.Error
			return nil
		}
		part.State = "output-available"
		part.Output = append(json.RawMessage(nil), decision.Result.Output...)
	}
	return nil
}

func jsonValue(text string) (json.RawMessage, error) {
	raw := json.RawMessage(text)
	if json.Valid(raw) {
		return raw, nil
	}
	payload, err := json.Marshal(text)
	return payload, err
}
