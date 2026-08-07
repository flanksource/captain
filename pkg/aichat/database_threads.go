package aichat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/google/uuid"
)

type DatabaseThreadStore struct {
	db *database.DB
}

func NewDatabaseThreadStore(db *database.DB) (*DatabaseThreadStore, error) {
	if db == nil || db.Gorm() == nil {
		return nil, fmt.Errorf("captain chat session store requires a database")
	}
	return &DatabaseThreadStore{db: db}, nil
}

func (s *DatabaseThreadStore) Create(ctx context.Context, title string) (*Thread, error) {
	metadata := map[string]any{"aichat": true}
	// A caller who names a thread up front owns that name, so later automatic
	// naming leaves it alone.
	if strings.TrimSpace(title) != "" {
		metadata[database.SessionTitleSourceKey] = string(database.SessionTitleUser)
	}
	record, err := s.db.CreateOrGetSession(ctx, database.CreateSessionInput{
		ID: uuid.New(), Source: "aichat", Provider: "captain", HostID: "local",
		Title: strings.TrimSpace(title), Metadata: metadata,
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, record.ID.String())
}

func (s *DatabaseThreadStore) List(ctx context.Context) ([]*Thread, error) {
	overviews, err := s.db.ListSessionOverviews(ctx, database.SessionOverviewFilter{Source: "aichat", RootsOnly: true})
	if err != nil {
		return nil, err
	}
	threads := make([]*Thread, len(overviews))
	for i := range overviews {
		aggregate, err := s.getSession(ctx, overviews[i])
		if err != nil {
			return nil, err
		}
		threads[i] = threadFromSession(aggregate, overviews[i])
	}
	return threads, nil
}

func (s *DatabaseThreadStore) Get(ctx context.Context, id string) (*Thread, error) {
	overview, err := s.getOverview(ctx, id)
	if err != nil {
		return nil, err
	}
	if overview.Source != "aichat" {
		return nil, fmt.Errorf("captain chat session %s has source %q", overview.ID, overview.Source)
	}
	aggregate, err := s.getSession(ctx, *overview)
	if err != nil {
		return nil, err
	}
	return threadFromSession(aggregate, *overview), nil
}

func (s *DatabaseThreadStore) GetSession(ctx context.Context, id string) (*session.Session, error) {
	overview, err := s.getOverview(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.getSession(ctx, *overview)
}

func (s *DatabaseThreadStore) getOverview(ctx context.Context, id string) (*database.SessionOverview, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(id))
	if err != nil {
		return nil, fmt.Errorf("captain chat session ID %q is not a UUID: %w", id, err)
	}
	overview, err := s.db.GetSessionOverviewByIdentity(ctx, parsed.String())
	if err != nil {
		return nil, err
	}
	if overview.ID != parsed {
		return nil, fmt.Errorf("captain session %s resolved to %s", parsed, overview.ID)
	}
	return overview, nil
}

func (s *DatabaseThreadStore) getSession(ctx context.Context, overview database.SessionOverview) (*session.Session, error) {
	messages, err := s.db.ListTranscriptMessages(ctx, database.TranscriptPage{SessionID: overview.ID})
	if err != nil {
		return nil, err
	}
	turns, err := s.db.ListThreadTurns(ctx, overview.ID)
	if err != nil {
		return nil, err
	}
	requests, err := s.db.ListTurnRequests(ctx, database.TurnRequestFilter{SessionID: overview.ID})
	if err != nil {
		return nil, err
	}
	costs, err := s.db.ListThreadCosts(ctx, overview.ID)
	if err != nil {
		return nil, err
	}
	agents, err := s.db.ListThreadAgents(ctx, overview.ID)
	if err != nil {
		return nil, err
	}
	aggregate := sessionFromOverview(overview)
	aggregate.Root, aggregate.Agents = projectSessionAgents(agents)
	if err := ApplyOverviewProjection(ctx, s.db, overview, aggregate); err != nil {
		return nil, err
	}
	// The overview's own usage/cost is root-scoped; a thread's subagents spend
	// against the same conversation, so roll the thread-wide figures on top.
	applyThreadCosts(aggregate, costs)
	aggregate.Messages, err = projectSessionMessages(messages)
	if err != nil {
		return nil, err
	}
	aggregate.Turns = projectSessionTurns(turns, aggregate.Messages)
	aggregate.Requests, err = projectSessionRequests(requests)
	if err != nil {
		return nil, err
	}
	applyRequestState(aggregate)
	return aggregate, nil
}

func (s *DatabaseThreadStore) AppendMessage(ctx context.Context, id string, message UIMessage) error {
	return s.putMessage(ctx, id, message, false)
}

func (s *DatabaseThreadStore) ReplaceLastMessage(ctx context.Context, id string, message UIMessage) error {
	thread, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := validateLastMessageReplacement(thread.Messages, message); err != nil {
		return err
	}
	return s.putMessage(ctx, id, message, true)
}

func (s *DatabaseThreadStore) putMessage(ctx context.Context, id string, message UIMessage, replace bool) error {
	sessionID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("captain chat session ID %q is not a UUID: %w", id, err)
	}
	turnID, err := uuid.Parse(message.TurnID)
	if err != nil {
		return fmt.Errorf("captain chat message %q turn ID %q is not a UUID: %w", message.ID, message.TurnID, err)
	}
	parts, err := json.Marshal(message.Parts)
	if err != nil {
		return fmt.Errorf("encode Captain chat message %q: %w", message.ID, err)
	}
	return s.db.PutChatMessage(ctx, database.PutChatMessageInput{
		SessionID: sessionID, TurnID: turnID, ProviderMessageID: message.ID,
		Role: message.Role, Parts: parts, Replace: replace,
	})
}

func (s *DatabaseThreadStore) Delete(ctx context.Context, id string) error {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("captain chat session ID %q is not a UUID: %w", id, err)
	}
	return s.db.DeleteChatSession(ctx, parsed)
}

func (s *DatabaseThreadStore) SetProviderSession(ctx context.Context, id, providerSessionID string) error {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("captain chat session ID %q is not a UUID: %w", id, err)
	}
	record, err := s.db.GetSession(ctx, parsed)
	if err != nil {
		return err
	}
	providerSessionID = strings.TrimSpace(providerSessionID)
	if providerSessionID == "" {
		return fmt.Errorf("provider session ID cannot be empty")
	}
	if record.ProviderSessionID != "" {
		if record.ProviderSessionID == providerSessionID {
			return nil
		}
		return fmt.Errorf("provider session ID is already bound to %q", record.ProviderSessionID)
	}
	_, err = s.db.UpdateSessionState(ctx, database.UpdateSessionStateInput{
		ID: parsed, ExpectedVersion: record.StateVersion, ProviderSessionID: &providerSessionID,
	})
	return err
}

func (s *DatabaseThreadStore) SetTitle(ctx context.Context, id string, update TitleUpdate) error {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("captain chat session ID %q is not a UUID: %w", id, err)
	}
	title, err := normalizeTitle(update)
	if err != nil {
		return err
	}
	_, err = s.db.SetSessionTitle(ctx, database.SetSessionTitleInput{
		ID: parsed, Title: title, Source: database.SessionTitleSource(update.Source),
	})
	return err
}

func (s *DatabaseThreadStore) AddUsage(ctx context.Context, id string, _ TurnUsage) (*Thread, error) {
	return s.Get(ctx, id)
}

func sessionFromOverview(overview database.SessionOverview) *session.Session {
	detail := &session.Session{
		ID: overview.ID.String(), ProviderSessionID: stringPointer(overview.ProviderSessionID), Revision: overview.StateVersion,
		LifecycleStatus: overview.LifecycleStatus, ActivityState: overview.ActivityState,
		HealthState: overview.HealthState, StateReason: stringPointer(overview.StateReason),
		Source: overview.Source, Project: stringPointer(overview.Project), CWD: stringPointer(overview.CWD),
		Slug: stringPointer(overview.Slug), Title: stringPointer(overview.Title), InitialPrompt: stringPointer(overview.InitialPrompt),
		Version: stringPointer(overview.CLIVersion), Provider: overview.Provider, Backend: stringPointer(overview.Backend),
		Model: stringPointer(overview.Model), ReasoningEffort: stringPointer(overview.Effort),
		ExecutionMode: api.RuntimeMode(overview.ExecutionMode),
		HistoryFile:   stringPointer(overview.HistoryFile), StartedAt: overview.StartedAt, EndedAt: overview.EndedAt,
		Usage: api.Usage{
			InputTokens: int(overview.InputTokens), OutputTokens: int(overview.OutputTokens),
			ReasoningTokens: int(overview.ReasoningTokens), CacheReadTokens: int(overview.CacheReadTokens),
			CacheWriteTokens: int(overview.CacheWriteTokens),
		},
		// overview.CostUSD is the resolved total, which already falls back to list
		// price per call — assigning it to ProviderCostUSD would make every
		// reconstruction claim to be a billed figure. Carry both readings and let
		// Cost.Total() resolve them the same way the view does.
		Cost: api.Cost{
			Model: stringPointer(overview.Model), InputTokens: int(overview.InputTokens), OutputTokens: int(overview.OutputTokens),
			ReasoningTokens: int(overview.ReasoningTokens), CacheReadTokens: int(overview.CacheReadTokens),
			CacheWriteTokens: int(overview.CacheWriteTokens), TotalTokens: int(overview.TotalTokens),
			InputCost: overview.InputCost, OutputCost: overview.OutputCost, ReasoningCost: overview.ReasoningCost,
			CacheReadCost: overview.CacheReadCost, CacheWriteCost: overview.CacheWriteCost,
			ProviderCostUSD: overview.ProviderCostUSD,
		},
	}
	return detail
}

func projectSessionMessages(rows []database.TranscriptMessage) ([]session.Message, error) {
	messages := make([]session.Message, len(rows))
	for i := range rows {
		if err := json.Unmarshal(rows[i].Parts, &messages[i].Parts); err != nil {
			return nil, fmt.Errorf("decode Captain message %s parts: %w", rows[i].ID, err)
		}
		messages[i].ID = stringPointer(rows[i].ProviderMessageID)
		if messages[i].ID == "" {
			messages[i].ID = rows[i].ID.String()
		}
		messages[i].Role = rows[i].Role
		if rows[i].TurnID != nil {
			messages[i].TurnID = rows[i].TurnID.String()
		}
	}
	return messages, nil
}

func projectSessionTurns(rows []database.SessionTurn, messages []session.Message) []session.Turn {
	messageIDs := make(map[string][]string)
	for _, message := range messages {
		messageIDs[message.TurnID] = append(messageIDs[message.TurnID], message.ID)
	}
	turns := make([]session.Turn, len(rows))
	for i := range rows {
		turns[i] = session.Turn{
			ID: rows[i].ID.String(), Status: rows[i].Status, Index: rows[i].TurnIndex,
			StartedAt: rows[i].StartedAt, EndedAt: rows[i].EndedAt,
			StopReason: stringPointer(rows[i].StopReason), Model: stringPointer(rows[i].Model),
			Backend: stringPointer(rows[i].Backend), ReasoningEffort: stringPointer(rows[i].Effort),
			MessageIDs: messageIDs[rows[i].ID.String()],
			Usage: api.Usage{
				InputTokens: int(rows[i].InputTokens), OutputTokens: int(rows[i].OutputTokens),
				ReasoningTokens: int(rows[i].ReasoningTokens), CacheReadTokens: int(rows[i].CacheReadTokens),
				CacheWriteTokens: int(rows[i].CacheWriteTokens),
			},
			Cost: api.Cost{
				Model: stringPointer(rows[i].Model), TotalTokens: int(rows[i].TotalTokens),
				InputCost: rows[i].InputCost, OutputCost: rows[i].OutputCost, ReasoningCost: rows[i].ReasoningCost,
				CacheReadCost: rows[i].CacheReadCost, CacheWriteCost: rows[i].CacheWriteCost,
				ProviderCostUSD: rows[i].ProviderCostUSD,
			},
		}
	}
	return turns
}

func projectSessionRequests(rows []database.TurnRequest) ([]session.Request, error) {
	requests := make([]session.Request, len(rows))
	for i := range rows {
		input, err := json.Marshal(rows[i].Request["input"])
		if err != nil {
			return nil, fmt.Errorf("encode Captain request %s input: %w", rows[i].ID, err)
		}
		updatedInput, err := json.Marshal(rows[i].Response["updatedInput"])
		if err != nil {
			return nil, fmt.Errorf("encode Captain request %s updated input: %w", rows[i].ID, err)
		}
		requests[i] = session.Request{
			ID: rows[i].ID.String(), ToolCallID: rows[i].ToolCallID, Kind: rows[i].Kind, State: string(rows[i].State),
			Tool: fmt.Sprint(rows[i].Request["tool"]), Input: input, RequestedBy: rows[i].RequestedBy,
			ResolvedBy: rows[i].ResolvedBy, Reason: rows[i].Reason, Version: rows[i].Version,
			ExpiresAt: rows[i].ExpiresAt, CreatedAt: rows[i].CreatedAt, ResolvedAt: rows[i].ResolvedAt,
		}
		if rows[i].Response != nil && rows[i].Response["updatedInput"] != nil {
			requests[i].UpdatedInput = updatedInput
		}
		if rows[i].TurnID != nil {
			requests[i].TurnID = rows[i].TurnID.String()
		}
		if rows[i].PromptRunID != nil {
			requests[i].PromptRunID = rows[i].PromptRunID.String()
		}
		if rows[i].ModelCallID != nil {
			requests[i].ModelCallID = rows[i].ModelCallID.String()
		}
	}
	return requests, nil
}

func applyRequestState(aggregate *session.Session) {
	byID := make(map[string]session.Request, len(aggregate.Requests))
	for _, request := range aggregate.Requests {
		byID[request.ID] = request
		switch request.State {
		case string(database.TurnRequestStateApproved):
			aggregate.Approvals.Approved++
		case string(database.TurnRequestStateDenied):
			aggregate.Approvals.Denied++
			aggregate.Approvals.Denials = append(aggregate.Approvals.Denials, session.Denial{
				ToolUseID: request.ToolCallID, Tool: request.Tool, Reason: request.Reason,
			})
		}
	}
	for i := range aggregate.Messages {
		for j := range aggregate.Messages[i].Parts {
			part := &aggregate.Messages[i].Parts[j]
			if part.Approval == nil {
				continue
			}
			request, ok := byID[part.Approval.ID]
			if !ok {
				continue
			}
			switch request.State {
			case string(database.TurnRequestStatePending):
				part.State = session.ToolStateApprovalRequested
			case string(database.TurnRequestStateApproved):
				approved := true
				if part.State == session.ToolStateApprovalRequested || part.State == session.ToolStateApprovalResponded {
					part.State = session.ToolStateApprovalResponded
				}
				part.Approval.Approved = &approved
				part.Approval.Reason = request.Reason
			case string(database.TurnRequestStateDenied):
				approved := false
				part.State = session.ToolStateOutputDenied
				part.Approval.Approved = &approved
				part.Approval.Reason = request.Reason
			case string(database.TurnRequestStateCancelled):
				approved := false
				part.State = session.ToolStateOutputDenied
				part.Approval.Approved = &approved
				part.Approval.Reason = request.Reason
			}
		}
	}
}

func threadFromSession(aggregate *session.Session, overview database.SessionOverview) *Thread {
	messages := make([]UIMessage, len(aggregate.Messages))
	for i := range aggregate.Messages {
		parts := make([]UIPart, len(aggregate.Messages[i].Parts))
		for j := range aggregate.Messages[i].Parts {
			part := aggregate.Messages[i].Parts[j]
			parts[j] = UIPart{
				Type: part.Type, Text: part.Text, MediaType: part.MediaType, URL: part.URL, Filename: part.Filename,
				AttachmentID: part.AttachmentID, ToolName: part.ToolName, ToolCallID: part.ToolCallID,
				State: part.State, Input: part.Input, Output: part.Output, ErrorText: part.ErrorText, Data: part.Data,
			}
			if part.Approval != nil {
				parts[j].Approval = &Approval{ID: part.Approval.ID, Approved: part.Approval.Approved, Reason: part.Approval.Reason}
			}
		}
		messages[i] = UIMessage{
			ID: aggregate.Messages[i].ID, Role: aggregate.Messages[i].Role,
			Parts: parts, TurnID: aggregate.Messages[i].TurnID,
		}
	}
	return &Thread{
		ID: aggregate.ID, Title: aggregate.Title, CreatedAt: overview.CreatedAt, UpdatedAt: overview.UpdatedAt,
		Messages: messages, TotalInputTokens: aggregate.Usage.InputTokens, TotalOutputTokens: aggregate.Usage.OutputTokens,
		TotalReasoningTokens: aggregate.Usage.ReasoningTokens, TotalCacheReadTokens: aggregate.Usage.CacheReadTokens,
		TotalCacheWriteTokens: aggregate.Usage.CacheWriteTokens, TotalCostUSD: aggregate.Cost.Total(),
		LastContextTokens: intPointer(overview.ContextTokens), ProviderSessionID: aggregate.ProviderSessionID,
	}
}

func stringPointer(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func intPointer(value *int64) int {
	if value == nil {
		return 0
	}
	return int(*value)
}
