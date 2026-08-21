package aichat

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/flanksource/captain/pkg/api"
)

type assistantMessageBuilder struct {
	message          UIMessage
	toolParts        map[string]int
	terminalMetadata terminalMetadataContext
	sessionID        string
	model            string
	replace          bool
}

type assistantMessageBuilderOptions struct {
	MessageID        string
	TurnID           string
	Replace          bool
	Seed             *UIMessage
	Resume           *api.ToolApprovalResume
	terminalMetadata terminalMetadataContext
}

func newAssistantMessageBuilder(options assistantMessageBuilderOptions) (*assistantMessageBuilder, error) {
	builder := &assistantMessageBuilder{
		message:          UIMessage{ID: options.MessageID, TurnID: options.TurnID, Role: string(api.RoleAssistant), Parts: []UIPart{}},
		toolParts:        map[string]int{},
		terminalMetadata: options.terminalMetadata,
		replace:          options.Replace || options.Resume != nil,
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

// persistedEventOptions carries the turn identity a persisted stream needs:
// which thread and turn it belongs to, which model produced it (for pricing),
// and where to record the resulting costs for the finish part.
type persistedEventOptions struct {
	Request          ChatRequest
	TurnID           string
	Model            api.Model
	Runtime          func() api.Model
	Costs            *TurnCosts
	terminalMetadata terminalMetadataContext
}

func (o persistedEventOptions) runtime() api.Model {
	if o.Runtime != nil {
		return o.Runtime()
	}
	return o.Model
}

func (s *Service) persistedEvents(ctx context.Context, options persistedEventOptions, source <-chan api.Event) <-chan api.Event {
	request, turnID := options.Request, options.TurnID
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
		messageID := assistantMessageID(request, turnID)
		replace := request.Trigger == "regenerate-message"
		builder, err := newAssistantMessageBuilder(assistantMessageBuilderOptions{
			MessageID: messageID, TurnID: turnID, Replace: replace, Seed: seed, Resume: request.ToolApproval,
			terminalMetadata: options.terminalMetadata,
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
				if err := s.persistEvent(ctx, request.ThreadID, event, options.runtime(), options.Costs); err != nil {
					sendEvent(ctx, out, api.Event{Kind: api.EventError, Error: err.Error(), Model: event.Model})
					return
				}
			}
			terminal := event.Kind == api.EventResult || event.Kind == api.EventError || event.Kind == api.EventInterrupted
			if !persisted && terminal {
				if err := s.persistCompletedTurn(ctx, request.ThreadID, builder, event, options); err != nil {
					sendEvent(ctx, out, api.Event{Kind: api.EventError, Error: err.Error(), Model: event.Model})
					return
				}
				persisted = true
			}
			if !sendEvent(ctx, out, event) {
				return
			}
			if terminal {
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
	store, err := s.threads(ctx)
	if err != nil {
		return nil, err
	}
	thread, err := store.Get(ctx, request.ThreadID)
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

func (s *Service) persistCompletedTurn(
	ctx context.Context,
	threadID string,
	builder *assistantMessageBuilder,
	event api.Event,
	options persistedEventOptions,
) error {
	if err := s.persistEvent(ctx, threadID, event, options.runtime(), options.Costs); err != nil {
		return err
	}
	if builder.message.Metadata != nil && options.Costs != nil {
		builder.message.Metadata.CostBreakdown = options.Costs.Breakdown
		builder.message.Metadata.ThreadCostUSD = options.Costs.ThreadCostUSD
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
	store, err := s.threads(ctx)
	if err != nil {
		return err
	}
	if builder.replace {
		err = store.ReplaceLastMessage(ctx, threadID, builder.message)
	} else {
		err = store.AppendMessage(ctx, threadID, builder.message)
	}
	if err != nil {
		return err
	}
	// Backends that name sessions themselves (Claude's SessionTitle) report it as
	// a tool call rather than through Captain's own tool handler.
	s.setThreadTitle(ctx, threadID, TitleUpdate{Title: agentTitle(builder.message), Source: TitleSourceAI})
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
		b.terminalizeTools(event.Error)
		b.message.Metadata = b.terminalMetadata.message(b.sessionID, b.model, false)
		payload, err := json.Marshal(map[string]string{"error": event.Error})
		if err != nil {
			return err
		}
		b.message.Parts = append(b.message.Parts, UIPart{Type: "data-error", Data: payload})
	case api.EventInterrupted:
		return b.interrupted(event.Reason)
	case api.EventSystem:
	}
	return nil
}

func (b *assistantMessageBuilder) interrupted(reason string) error {
	b.terminalizeTools(reason)
	data, err := json.Marshal(map[string]bool{"success": false, "interrupted": true})
	if err != nil {
		return err
	}
	b.message.Parts = append(b.message.Parts, UIPart{Type: "data-result", Data: data})
	b.message.Metadata = b.terminalMetadata.message(b.sessionID, b.model, false)
	b.message.Metadata.Interrupted = true
	return nil
}

func (b *assistantMessageBuilder) terminalizeTools(message string) {
	if message == "" {
		message = "tool execution did not complete"
	}
	for i := range b.message.Parts {
		part := &b.message.Parts[i]
		if !part.IsTool() {
			continue
		}
		switch part.State {
		case "output-available", "output-error", "output-denied":
			continue
		}
		part.State = "output-error"
		part.Output = nil
		part.ErrorText = message
		part.Approval = nil
	}
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
	if event.ApprovalID == "" {
		return fmt.Errorf("persist tool approval %q has no durable approval ID", event.ToolCallID)
	}
	part, err := b.toolPart(event.ToolCallID, event.Tool)
	if err != nil {
		return err
	}
	part.State = "approval-requested"
	part.Approval = &Approval{ID: event.ApprovalID}
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
		var err error
		data, err = json.Marshal(map[string]bool{"success": event.Success, "waitingApproval": true})
		if err != nil {
			return fmt.Errorf("marshal tool approval result state: %w", err)
		}
	} else if len(data) == 0 {
		var err error
		data, err = json.Marshal(map[string]bool{"success": event.Success})
		if err != nil {
			return fmt.Errorf("marshal result state: %w", err)
		}
	}
	b.message.Parts = append(b.message.Parts, UIPart{Type: dataType, Data: data})
	b.message.Metadata = b.terminalMetadata.message(b.sessionID, b.model, event.Success)
	b.message.Metadata.Cost = event.CostUSD
	if event.Usage != nil {
		b.message.Metadata.Usage = usageMetadata(*event.Usage)
		b.message.Metadata.ContextTokens = contextTokens(*event.Usage)
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
		part.Approval = &Approval{ID: decision.ApprovalID, Approved: &approved}
		if len(decision.Input) > 0 {
			part.Input = append(json.RawMessage(nil), decision.Input...)
		}
	case api.ToolApprovalDeny:
		approved := false
		part.State = "output-denied"
		part.Approval = &Approval{
			ID: decision.ApprovalID, Approved: &approved, Reason: decision.Message,
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

func equalPartJSON(left, right json.RawMessage) bool {
	if len(left) == 0 || len(right) == 0 {
		return len(left) == len(right)
	}
	var leftValue, rightValue any
	return json.Unmarshal(left, &leftValue) == nil &&
		json.Unmarshal(right, &rightValue) == nil &&
		reflect.DeepEqual(leftValue, rightValue)
}
