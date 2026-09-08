package aichat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	sessionquery "github.com/flanksource/captain/pkg/session/query"
	"github.com/google/uuid"
)

const (
	threadRuntimeMetadataKey = "aichatRuntime"
	forkedFromMetadataKey    = "forkedFrom"
)

type DatabaseThreadStore struct {
	db    *database.DB
	query *sessionquery.Loader
}

var ErrHistoryUnavailable = sessionquery.ErrHistoryUnavailable

func NewDatabaseThreadStore(db *database.DB) (*DatabaseThreadStore, error) {
	loader, err := sessionquery.New(db)
	if err != nil {
		return nil, fmt.Errorf("captain chat session store requires a database: %w", err)
	}
	return &DatabaseThreadStore{db: db, query: loader}, nil
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
	overviews, err := s.db.ListSessionOverviews(ctx, database.SessionOverviewFilter{
		Source: "aichat", RootsOnly: true, Limit: maxThreadSummaries,
	})
	if err != nil {
		return nil, err
	}
	threads := make([]*Thread, len(overviews))
	for i := range overviews {
		threads[i], err = threadSummaryFromOverview(overviews[i])
		if err != nil {
			return nil, err
		}
		if overviews[i].AgentCount > 1 {
			costs, costErr := s.db.ListThreadCosts(ctx, overviews[i].ID)
			if costErr != nil {
				return nil, costErr
			}
			applyThreadSummaryCosts(threads[i], costs)
		}
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
	return threadFromSession(aggregate, *overview)
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
		if errors.Is(err, database.ErrSessionNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrThreadNotFound, parsed)
		}
		return nil, err
	}
	if overview.ID != parsed {
		return nil, fmt.Errorf("captain session %s resolved to %s", parsed, overview.ID)
	}
	return overview, nil
}

// getSession composes the chat route's aggregate through the shared assembler.
//
// It supplies the stored source — the rows only this store knows how to read —
// and lets ComposeSession add the prompt run, the tool approvals and the stored
// projection, exactly as the session route does. Previously this function built
// its own aggregate end to end, which is how the two routes came to disagree
// about the same session.
func (s *DatabaseThreadStore) getSession(ctx context.Context, overview database.SessionOverview) (*session.Session, error) {
	result, _, err := s.query.Load(ctx, overview, sessionquery.LoadOptions{SuppressPromptRunMessages: true})
	if err != nil {
		return nil, err
	}
	return result.Session, nil
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
	turnID := uuid.Nil
	if strings.TrimSpace(message.TurnID) != "" {
		turnID, err = uuid.Parse(message.TurnID)
		if err != nil {
			return fmt.Errorf("captain chat message %q turn ID %q is not a UUID: %w", message.ID, message.TurnID, err)
		}
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

func (s *DatabaseThreadStore) SetRuntime(ctx context.Context, id string, runtime api.Model) error {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("captain chat session ID %q is not a UUID: %w", id, err)
	}
	identity, err := threadRuntimeIdentity(runtime)
	if err != nil {
		return err
	}
	// Stored as a runtime selection, not a bare api.Model: Model.Provider is
	// json:"-" (it carries the whole catalog), so persisting the model would drop
	// the family half of the runtime, and the write-once key would then compare a
	// providerless record against a resolved one on read.
	if err := s.db.SetSessionMetadataOnce(ctx, parsed, threadRuntimeMetadataKey,
		runtimeSelection(identity.ToModel())); err != nil {
		if errors.Is(err, database.ErrSessionConflict) {
			return fmt.Errorf("%w: %v", ErrThreadRuntimeConflict, err)
		}
		return err
	}
	return nil
}

func (s *DatabaseThreadStore) Fork(ctx context.Context, id string) (*Thread, error) {
	source, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	title, seed, err := forkSeedMessage(source)
	if err != nil {
		return nil, err
	}
	parts, err := json.Marshal(seed.Parts)
	if err != nil {
		return nil, fmt.Errorf("encode fork seed: %w", err)
	}
	forkID := uuid.New()
	if _, err := s.db.ForkChatSession(ctx, database.ForkChatSessionInput{
		SourceSessionID: sourceID(source), ExpectedSourceUpdatedAt: source.UpdatedAt,
		SessionID: forkID, Title: title,
		Metadata: map[string]any{
			"aichat": true, forkedFromMetadataKey: source.ID,
			database.SessionTitleSourceKey: string(database.SessionTitleDerived),
		},
		ProviderMessageID: seed.ID, Role: seed.Role, Parts: parts,
	}); err != nil {
		return nil, err
	}
	return s.Get(ctx, forkID.String())
}

func sourceID(thread *Thread) uuid.UUID {
	return uuid.MustParse(thread.ID)
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

func threadFromSession(aggregate *session.Session, overview database.SessionOverview) (*Thread, error) {
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
	thread, err := threadSummaryFromOverview(overview)
	if err != nil {
		return nil, err
	}
	thread.Messages = messages
	thread.TotalInputTokens = aggregate.Usage.InputTokens
	thread.TotalOutputTokens = aggregate.Usage.OutputTokens
	thread.TotalReasoningTokens = aggregate.Usage.ReasoningTokens
	thread.TotalCacheReadTokens = aggregate.Usage.CacheReadTokens
	thread.TotalCacheWriteTokens = aggregate.Usage.CacheWriteTokens
	thread.TotalCostUSD = aggregate.Cost.Total()
	thread.ProviderSessionID = aggregate.ProviderSessionID
	return thread, nil
}

func threadSummaryFromOverview(overview database.SessionOverview) (*Thread, error) {
	runtime, forkedFrom, err := threadIdentityMetadata(overview.Metadata)
	if err != nil {
		return nil, fmt.Errorf("decode Captain chat session %s metadata: %w", overview.ID, err)
	}
	// Summaries intentionally carry no transcript. GET /sessions/{id} is the
	// only hydration path, keeping the picker bounded as threads grow.
	return &Thread{
		ID: overview.ID.String(), Title: stringPointer(overview.Title), Revision: overview.StateVersion,
		CreatedAt: overview.CreatedAt, UpdatedAt: overview.UpdatedAt,
		Runtime: runtime, ForkedFrom: forkedFrom,
		Messages: nil, TotalInputTokens: int(overview.InputTokens), TotalOutputTokens: int(overview.OutputTokens),
		TotalReasoningTokens: int(overview.ReasoningTokens), TotalCacheReadTokens: int(overview.CacheReadTokens),
		TotalCacheWriteTokens: int(overview.CacheWriteTokens), TotalCostUSD: overview.CostUSD,
		LastContextTokens: intPointer(overview.ContextTokens), ProviderSessionID: stringPointer(overview.ProviderSessionID),
	}, nil
}

func threadIdentityMetadata(raw json.RawMessage) (*api.RuntimeIdentity, string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, "", nil
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return nil, "", err
	}
	var runtime *api.RuntimeIdentity
	if value := metadata[threadRuntimeMetadataKey]; len(value) > 0 {
		var decoded database.PromptRunRuntimeSelection
		if err := json.Unmarshal(value, &decoded); err != nil {
			return nil, "", err
		}
		identity, err := threadRuntimeIdentity(runtimeModel(decoded, api.Model{}))
		if err != nil {
			return nil, "", err
		}
		runtime = &identity
	}
	var forkedFrom string
	if value := metadata[forkedFromMetadataKey]; len(value) > 0 {
		if err := json.Unmarshal(value, &forkedFrom); err != nil {
			return nil, "", err
		}
	}
	return runtime, forkedFrom, nil
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
