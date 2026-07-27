package aichat

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flanksource/captain/pkg/api"
)

type assistantMessageBuilder struct {
	message   UIMessage
	toolParts map[string]int
	sessionID string
	model     string
}

func newAssistantMessageBuilder(chatID string) *assistantMessageBuilder {
	id := ""
	if chatID != "" {
		id = chatID + "-assistant"
	}
	return &assistantMessageBuilder{
		message:   UIMessage{ID: id, Role: string(api.RoleAssistant), Parts: []UIPart{}},
		toolParts: map[string]int{},
	}
}

func (s *Service) persistedEvents(ctx context.Context, request ChatRequest, source <-chan api.Event) <-chan api.Event {
	if request.ThreadID == "" {
		return source
	}
	out := make(chan api.Event)
	go func() {
		defer close(out)
		builder := newAssistantMessageBuilder(request.ID)
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
			if err := s.options.Threads.AppendMessage(ctx, request.ThreadID, builder.message); err != nil {
				sendEvent(ctx, out, api.Event{Kind: api.EventError, Error: fmt.Sprintf("persist assistant message: %v", err)})
			}
		}
	}()
	return out
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
	if err := s.options.Threads.AppendMessage(ctx, threadID, builder.message); err != nil {
		return fmt.Errorf("persist assistant message: %w", err)
	}
	return nil
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
	if _, exists := b.toolParts[event.ToolCallID]; exists {
		return fmt.Errorf("persist duplicate tool call %q", event.ToolCallID)
	}
	input, err := json.Marshal(event.Input)
	if err != nil {
		return fmt.Errorf("marshal tool %q input: %w", event.Tool, err)
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

func jsonValue(text string) (json.RawMessage, error) {
	raw := json.RawMessage(text)
	if json.Valid(raw) {
		return raw, nil
	}
	payload, err := json.Marshal(text)
	return payload, err
}
