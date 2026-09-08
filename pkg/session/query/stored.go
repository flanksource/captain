package query

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/captain/pkg/session/load"
	"github.com/google/uuid"
)

const (
	threadRuntimeMetadataKey = "aichatRuntime"
	forkedFromMetadataKey    = "forkedFrom"
)

type Loader struct {
	db *database.DB
}

// LoadOptions controls transcript and prompt-run projection for a stored
// session load.
type LoadOptions struct {
	Transcript                *session.Session
	SuppressPromptRunMessages bool
}

// OverviewStore resolves Captain and provider session identities.
type OverviewStore interface {
	ListSessionOverviewsByIdentity(context.Context, string) ([]database.SessionOverview, error)
	ListThreadSessionOverviews(context.Context, uuid.UUID) ([]database.SessionOverview, error)
}

func New(db *database.DB) (*Loader, error) {
	if db == nil || db.Gorm() == nil {
		return nil, fmt.Errorf("captain session query requires a database")
	}
	return &Loader{db: db}, nil
}

func (q *Loader) Load(
	ctx context.Context,
	overview database.SessionOverview,
	opts LoadOptions,
) (load.Result, []error, error) {
	stored, err := q.StoredAggregate(ctx, overview)
	if err != nil {
		return load.Result{}, nil, err
	}
	return ComposeSession(ctx, q.db, overview, ComposeOptions{
		Stored: stored, Transcript: opts.Transcript,
		SuppressPromptRunMessages: opts.SuppressPromptRunMessages,
	})
}

func (q *Loader) StoredAggregate(ctx context.Context, overview database.SessionOverview) (*session.Session, error) {
	if overview.MessageCount == 0 {
		return nil, nil
	}
	messages, err := q.db.ListTranscriptMessages(ctx, database.TranscriptPage{SessionID: overview.ID})
	if err != nil {
		return nil, err
	}
	turns, err := q.db.ListThreadTurns(ctx, overview.ID)
	if err != nil {
		return nil, err
	}
	costs, err := q.db.ListThreadCosts(ctx, overview.ID)
	if err != nil {
		return nil, err
	}
	agents, err := q.db.ListThreadAgents(ctx, overview.ID)
	if err != nil {
		return nil, err
	}
	aggregate := sessionFromOverview(overview)
	aggregate.Root, aggregate.Agents = projectSessionAgents(agents)
	aggregate.Runtime, aggregate.ForkedFrom, err = threadIdentityMetadata(overview.Metadata)
	if err != nil {
		return nil, fmt.Errorf("decode Captain chat session %s metadata: %w", overview.ID, err)
	}
	applyThreadCosts(aggregate, costs)
	aggregate.Messages, err = projectSessionMessages(messages)
	if err != nil {
		return nil, err
	}
	aggregate.Messages, err = q.recoverMessages(ctx, overview, turns, aggregate.Messages)
	if err != nil {
		return nil, err
	}
	aggregate.Turns = projectSessionTurns(turns, aggregate.Messages)
	return aggregate, nil
}

func (q *Loader) Resolve(ctx context.Context, identity string) ([]database.SessionOverview, error) {
	return Resolve(ctx, q.db, identity)
}

// Resolve returns all overviews represented by a Captain UUID or provider
// session identity. A root Captain UUID expands to its complete thread.
func Resolve(ctx context.Context, db OverviewStore, identity string) ([]database.SessionOverview, error) {
	overviews, err := db.ListSessionOverviewsByIdentity(ctx, identity)
	if err != nil {
		return nil, err
	}
	parsed, parseErr := uuid.Parse(identity)
	if parseErr != nil || len(overviews) != 1 || overviews[0].ID != parsed ||
		overviews[0].ParentSessionID != nil || overviews[0].RootSessionID != nil {
		return overviews, nil
	}
	thread, err := db.ListThreadSessionOverviews(ctx, parsed)
	if err != nil {
		return nil, err
	}
	if len(thread) > 1 {
		return thread, nil
	}
	return overviews, nil
}

func sessionFromOverview(overview database.SessionOverview) *session.Session {
	return &session.Session{
		ID: overview.ID.String(), ProviderSessionID: stringValue(overview.ProviderSessionID), Revision: overview.StateVersion,
		LifecycleStatus: overview.LifecycleStatus, ActivityState: overview.ActivityState,
		HealthState: overview.HealthState, StateReason: stringValue(overview.StateReason),
		Source: overview.Source, Project: stringValue(overview.Project), CWD: stringValue(overview.CWD),
		Slug: stringValue(overview.Slug), Title: stringValue(overview.Title), InitialPrompt: stringValue(overview.InitialPrompt),
		Version: stringValue(overview.CLIVersion), Provider: overview.Provider,
		ModelMode: api.RuntimeMode(stringValue(overview.ModelMode)), Model: stringValue(overview.Model),
		ReasoningEffort: stringValue(overview.Effort), ExecutionMode: api.RuntimeMode(overview.ExecutionMode),
		HistoryFile: stringValue(overview.HistoryFile), StartedAt: overview.StartedAt, EndedAt: overview.EndedAt,
		Usage: api.Usage{
			InputTokens: int(overview.InputTokens), OutputTokens: int(overview.OutputTokens),
			ReasoningTokens: int(overview.ReasoningTokens), CacheReadTokens: int(overview.CacheReadTokens),
			CacheWriteTokens: int(overview.CacheWriteTokens),
		},
		Cost: api.Cost{
			Model: stringValue(overview.Model), InputTokens: int(overview.InputTokens), OutputTokens: int(overview.OutputTokens),
			ReasoningTokens: int(overview.ReasoningTokens), CacheReadTokens: int(overview.CacheReadTokens),
			CacheWriteTokens: int(overview.CacheWriteTokens), TotalTokens: int(overview.TotalTokens),
			InputCost: overview.InputCost, OutputCost: overview.OutputCost, ReasoningCost: overview.ReasoningCost,
			CacheReadCost: overview.CacheReadCost, CacheWriteCost: overview.CacheWriteCost,
			ProviderCostUSD: overview.ProviderCostUSD,
		},
	}
}

func projectSessionMessages(rows []database.TranscriptMessage) ([]session.Message, error) {
	messages := make([]session.Message, len(rows))
	for i := range rows {
		if err := json.Unmarshal(rows[i].Parts, &messages[i].Parts); err != nil {
			return nil, fmt.Errorf("decode Captain message %s parts: %w", rows[i].ID, err)
		}
		messages[i].ID = stringValue(rows[i].ProviderMessageID)
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
			StopReason: stringValue(rows[i].StopReason), Model: stringValue(rows[i].Model),
			ModelProvider: stringValue(rows[i].ModelProvider), Mode: stringValue(rows[i].ModelMode),
			ReasoningEffort: stringValue(rows[i].Effort), MessageIDs: messageIDs[rows[i].ID.String()],
			Usage: api.Usage{
				InputTokens: int(rows[i].InputTokens), OutputTokens: int(rows[i].OutputTokens),
				ReasoningTokens: int(rows[i].ReasoningTokens), CacheReadTokens: int(rows[i].CacheReadTokens),
				CacheWriteTokens: int(rows[i].CacheWriteTokens),
			},
			Cost: api.Cost{
				Model: stringValue(rows[i].Model), TotalTokens: int(rows[i].TotalTokens),
				InputCost: rows[i].InputCost, OutputCost: rows[i].OutputCost, ReasoningCost: rows[i].ReasoningCost,
				CacheReadCost: rows[i].CacheReadCost, CacheWriteCost: rows[i].CacheWriteCost,
				ProviderCostUSD: rows[i].ProviderCostUSD,
			},
		}
	}
	return turns
}

func projectSessionAgents(rows []database.SessionAgent) (*session.Agent, []*session.Agent) {
	byID := make(map[string]*session.Agent, len(rows))
	agents := make([]*session.Agent, 0, len(rows))
	for _, row := range rows {
		agent := &session.Agent{
			ID: row.SessionID.String(), Type: stringValue(row.AgentType), Desc: stringValue(row.Description),
			IsRoot: row.IsRoot, HistoryFile: stringValue(row.HistoryFile),
			Usage: api.Usage{
				InputTokens: int(row.InputTokens), OutputTokens: int(row.OutputTokens),
				ReasoningTokens: int(row.ReasoningTokens), CacheReadTokens: int(row.CacheReadTokens),
				CacheWriteTokens: int(row.CacheWriteTokens),
			},
			Cost: api.Cost{
				InputTokens: int(row.InputTokens), OutputTokens: int(row.OutputTokens), ReasoningTokens: int(row.ReasoningTokens),
				CacheReadTokens: int(row.CacheReadTokens), CacheWriteTokens: int(row.CacheWriteTokens),
				TotalTokens: int(row.TotalTokens), ProviderCostUSD: row.CostUSD,
			},
		}
		if row.ParentSessionID != nil {
			agent.ParentID = row.ParentSessionID.String()
		}
		byID[agent.ID] = agent
		agents = append(agents, agent)
	}
	var root *session.Agent
	for _, agent := range agents {
		if agent.IsRoot && root == nil {
			root = agent
		}
		if parent, found := byID[agent.ParentID]; found && agent.ParentID != agent.ID {
			parent.Children = append(parent.Children, agent)
		}
	}
	return root, agents
}

func applyThreadCosts(aggregate *session.Session, rows []database.SessionCost) {
	if len(rows) == 0 {
		return
	}
	costs := make(api.Costs, len(rows))
	for i := range rows {
		costs[i] = api.Cost{
			Model: rows[i].Model, InputTokens: int(rows[i].InputTokens), OutputTokens: int(rows[i].OutputTokens),
			ReasoningTokens: int(rows[i].ReasoningTokens), CacheReadTokens: int(rows[i].CacheReadTokens),
			CacheWriteTokens: int(rows[i].CacheWriteTokens), TotalTokens: int(rows[i].TotalTokens),
			InputCost: rows[i].InputCost, OutputCost: rows[i].OutputCost, ReasoningCost: rows[i].ReasoningCost,
			CacheReadCost: rows[i].CacheReadCost, CacheWriteCost: rows[i].CacheWriteCost,
			ProviderCostUSD: rows[i].ProviderCostUSD,
		}
	}
	total := costs.Sum()
	aggregate.Cost = total
	aggregate.Usage = api.Usage{
		InputTokens: total.InputTokens, OutputTokens: total.OutputTokens,
		ReasoningTokens: total.ReasoningTokens, CacheReadTokens: total.CacheReadTokens,
		CacheWriteTokens: total.CacheWriteTokens,
	}
	byModel := costs.ByModel()
	aggregate.ToolCosts = make(api.Costs, 0, len(byModel))
	for model, cost := range byModel {
		cost.Model = model
		aggregate.ToolCosts = append(aggregate.ToolCosts, cost)
	}
	sort.Slice(aggregate.ToolCosts, func(i, j int) bool {
		return aggregate.ToolCosts[i].Model < aggregate.ToolCosts[j].Model
	})
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
		var selection database.PromptRunRuntimeSelection
		if err := json.Unmarshal(value, &selection); err != nil {
			return nil, "", err
		}
		runtime = &api.RuntimeIdentity{
			Model: selection.Model, Provider: selection.Provider,
			Mode: api.RuntimeMode(selection.Mode), Effort: api.Effort(selection.Effort),
		}
	}
	var forkedFrom string
	if value := metadata[forkedFromMetadataKey]; len(value) > 0 {
		if err := json.Unmarshal(value, &forkedFrom); err != nil {
			return nil, "", err
		}
	}
	return runtime, forkedFrom, nil
}
