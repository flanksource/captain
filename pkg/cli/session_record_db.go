package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/google/uuid"
)

type sessionOverviewStore interface {
	ListSessionOverviewsByIdentity(context.Context, string) ([]database.SessionOverview, error)
	ListThreadSessionOverviews(context.Context, uuid.UUID) ([]database.SessionOverview, error)
}

type sessionListStore interface {
	ListSessionSummaries(context.Context, database.SessionListFilter) (database.SessionListPage, error)
}

// sessionRecordQuery narrows the DB-backed session record list.
type sessionRecordQuery struct {
	Source         string // "all", "claude", or "codex"
	ProjectRoot    string
	Query          string
	LiveOnly       bool
	ActivityFrom   *time.Time
	ActivityBefore *time.Time
	Limit          int
	Cursor         string
}

type sessionRecordPage struct {
	Records    []SessionRecord
	Total      int
	NextCursor string
}

// dbSessionRecords projects the bounded list-specific database shape onto the
// SessionRecord wire type. Transcript/detail metrics remain on the overview
// path used by session get.
func dbSessionRecords(ctx context.Context, db sessionListStore, q sessionRecordQuery) (sessionRecordPage, error) {
	filter := database.SessionListFilter{
		ProjectRoot:    q.ProjectRoot,
		Query:          q.Query,
		RootsOnly:      true,
		LiveOnly:       q.LiveOnly,
		ActivityFrom:   q.ActivityFrom,
		ActivityBefore: q.ActivityBefore,
		Limit:          q.Limit,
		Cursor:         q.Cursor,
	}
	if q.Source != "" && q.Source != "all" {
		filter.Source = q.Source
	}
	page, err := db.ListSessionSummaries(ctx, filter)
	if err != nil {
		return sessionRecordPage{}, err
	}
	records := make([]SessionRecord, len(page.Rows))
	for i := range page.Rows {
		records[i] = recordFromOverview(overviewFromSummary(page.Rows[i]))
	}
	return sessionRecordPage{Records: records, Total: int(page.Total), NextCursor: page.NextCursor}, nil
}

func dbAllSessionRecords(ctx context.Context, db sessionListStore, query sessionRecordQuery) (sessionRecordPage, error) {
	query.Cursor = ""
	result := sessionRecordPage{}
	seen := map[string]struct{}{}
	for {
		page, err := dbSessionRecords(ctx, db, query)
		if err != nil {
			return sessionRecordPage{}, err
		}
		if result.Total == 0 {
			result.Total = page.Total
		}
		result.Records = append(result.Records, page.Records...)
		if page.NextCursor == "" {
			return result, nil
		}
		if _, ok := seen[page.NextCursor]; ok {
			return sessionRecordPage{}, fmt.Errorf("captain session pagination repeated cursor %q", page.NextCursor)
		}
		seen[page.NextCursor] = struct{}{}
		query.Cursor = page.NextCursor
	}
}

// resolveOverviewsByIdentity resolves sessions by Captain UUID or
// provider-session-id prefix.
func resolveOverviewsByIdentity(ctx context.Context, db sessionOverviewStore, id string) ([]database.SessionOverview, error) {
	overviews, err := db.ListSessionOverviewsByIdentity(ctx, id)
	if err != nil {
		return nil, err
	}
	if parsed, parseErr := uuid.Parse(id); parseErr == nil && len(overviews) == 1 &&
		overviews[0].ID == parsed && overviews[0].ParentSessionID == nil && overviews[0].RootSessionID == nil {
		thread, threadErr := db.ListThreadSessionOverviews(ctx, parsed)
		if threadErr != nil {
			return nil, threadErr
		}
		if len(thread) > 1 {
			return thread, nil
		}
	}
	return overviews, nil
}

// candidateFromOverview adapts a DB overview row to the transcript-parsing
// candidate shape used by the detail and plan readers.
func candidateFromOverview(overview database.SessionOverview) sessionCandidate {
	path := stringOr(overview.HistoryFile, stringOr(overview.Path, ""))
	return sessionCandidate{
		record: SessionRecord{
			ID: stringOr(overview.ProviderSessionID, overview.ID.String()), Source: overview.Source, DetailAvailable: true,
		},
		path: path,
	}
}

// sessionOverviewMetadata is the monitor-owned projection stored in
// captain_sessions.metadata (see pkg/monitor sessionMetadata).
type sessionOverviewMetadata struct {
	Model     string                `json:"model,omitempty"`
	Provider  string                `json:"provider,omitempty"`
	Files     session.ChangedFiles  `json:"files,omitempty"`
	Approvals session.ApprovalStats `json:"approvals,omitempty"`
	Plan      *session.Plan         `json:"plan,omitempty"`
}

func overviewMetadata(overview database.SessionOverview) sessionOverviewMetadata {
	var metadata sessionOverviewMetadata
	if len(overview.Metadata) > 0 {
		_ = json.Unmarshal(overview.Metadata, &metadata)
	}
	return metadata
}

func overviewGitBranch(overview database.SessionOverview) string {
	if len(overview.Git) == 0 {
		return ""
	}
	var git struct {
		Branch string `json:"branch"`
	}
	_ = json.Unmarshal(overview.Git, &git)
	return git.Branch
}

func stringOr(value *string, fallback string) string {
	if value != nil && *value != "" {
		return *value
	}
	return fallback
}

func overviewFromSummary(summary database.SessionListSummary) database.SessionOverview {
	return database.SessionOverview{
		ID: summary.ID, ProviderSessionID: summary.ProviderSessionID, Source: summary.Source,
		Provider: summary.Provider, HostID: summary.HostID, ParentSessionID: summary.ParentSessionID,
		RootSessionID: summary.RootSessionID, Path: summary.Path, HistoryFile: summary.HistoryFile,
		Project: summary.Project, CWD: summary.CWD, Title: summary.Title, InitialPrompt: summary.InitialPrompt,
		Slug: summary.Slug, CLIVersion: summary.CLIVersion, LifecycleStatus: summary.LifecycleStatus,
		Git: summary.Git, Metadata: summary.Metadata, StartedAt: summary.StartedAt, EndedAt: summary.EndedAt,
		LastActivityAt: summary.LastActivityAt, PID: summary.PID, ProcessStatus: summary.ProcessStatus,
		ProcessCommand: summary.ProcessCommand, ProcessCWD: summary.ProcessCWD,
		ProcessStartedAt: summary.ProcessStartedAt, ProcessSampledAt: summary.ProcessSampledAt,
		LastHeartbeatAt: summary.LastHeartbeatAt, LeaseOwner: summary.LeaseOwner,
		LeaseExpiresAt: summary.LeaseExpiresAt, ProcessActive: summary.ProcessActive,
		CPUPercent: summary.CPUPercent, MemoryPercent: summary.MemoryPercent, Model: summary.Model,
		Backend: summary.Backend, Effort: summary.Effort, ContextTokens: summary.ContextTokens,
		ContextWindowTokens: summary.ContextWindowTokens, ContextFreePercent: summary.ContextFreePercent,
		InputTokens: summary.InputTokens, OutputTokens: summary.OutputTokens,
		CacheReadTokens: summary.CacheReadTokens, CacheWriteTokens: summary.CacheWriteTokens,
		TotalTokens: summary.TotalTokens, CostUSD: summary.CostUSD,
		MessageCount: summary.MessageCount, ToolCallCount: summary.ToolCallCount,
	}
}

// recordFromOverview projects one overview row to the SessionRecord wire shape.
func recordFromOverview(overview database.SessionOverview) SessionRecord {
	metadata := overviewMetadata(overview)
	path := stringOr(overview.HistoryFile, stringOr(overview.Path, ""))
	id := stringOr(overview.ProviderSessionID, overview.ID.String())
	record := SessionRecord{
		Key:             overview.ID.String(),
		ID:              id,
		Source:          overview.Source,
		Project:         stringOr(overview.Project, ""),
		Slug:            stringOr(overview.Slug, ""),
		Title:           stringOr(overview.Title, ""),
		InitialPrompt:   stringOr(overview.InitialPrompt, ""),
		StartedAt:       overview.StartedAt,
		EndedAt:         overview.LastActivityAt,
		Model:           stringOr(overview.Model, metadata.Model),
		ReasoningEffort: stringOr(overview.Effort, ""),
		Version:         stringOr(overview.CLIVersion, ""),
		GitBranch:       overviewGitBranch(overview),
		Provider:        firstNonEmpty(overview.Provider, metadata.Provider),
		Backend:         stringOr(overview.Backend, ""),
		LifecycleStatus: string(overview.LifecycleStatus),
		CWD:             stringOr(overview.CWD, ""),
		ToolCalls:       int(overview.ToolCallCount),
		Messages:        int(overview.MessageCount),
		DetailAvailable: path != "" || overview.PromptRunCount > 0,
		CostUSD:         overview.CostUSD,
	}
	if record.EndedAt == nil {
		record.EndedAt = overview.EndedAt
	}
	if overview.TotalTokens > 0 {
		record.Tokens = &SessionTokensWire{
			InputTokens:         int(overview.InputTokens),
			OutputTokens:        int(overview.OutputTokens),
			CacheReadTokens:     int(overview.CacheReadTokens),
			CacheCreationTokens: int(overview.CacheWriteTokens),
			TotalTokens:         int(overview.TotalTokens),
		}
	}
	if overview.ContextTokens != nil && *overview.ContextTokens > 0 {
		context := &SessionContextWire{UsedTokens: int(*overview.ContextTokens)}
		if overview.ContextWindowTokens != nil {
			context.WindowTokens = int(*overview.ContextWindowTokens)
		}
		if overview.ContextFreePercent != nil {
			context.FreePercent = *overview.ContextFreePercent
		}
		record.Context = context
	}
	if overview.ProcessActive {
		record.Live = liveWireFromOverview(overview, id, path)
		if record.CWD == "" {
			record.CWD = stringOr(overview.ProcessCWD, "")
		}
	}
	record.Health = deriveSessionHealth(record)
	return record
}

func liveWireFromOverview(overview database.SessionOverview, id, path string) *SessionLiveWire {
	status := stringOr(overview.ProcessStatus, "")
	_, active := processLiveStatus(status)
	live := &SessionLiveWire{
		Status:          status,
		Active:          active,
		CWD:             stringOr(overview.ProcessCWD, ""),
		Command:         stringOr(overview.ProcessCommand, ""),
		SessionID:       id,
		SessionFile:     path,
		StartedAt:       overview.ProcessStartedAt,
		SampledAt:       overview.ProcessSampledAt,
		LastHeartbeatAt: overview.LastHeartbeatAt,
		LeaseOwner:      stringOr(overview.LeaseOwner, ""),
		LeaseExpiresAt:  overview.LeaseExpiresAt,
		LastActivity:    overview.LastActivityAt,
	}
	if overview.PID != nil {
		live.PID = int(*overview.PID)
	}
	if overview.CPUPercent != nil {
		live.CPUPercent = *overview.CPUPercent
	}
	if overview.MemoryPercent != nil {
		live.MemoryPercent = *overview.MemoryPercent
	}
	return live
}

// processLiveStatus mirrors the monitor's ps-stat classification: zombies and
// stopped processes are present but not active.
func processLiveStatus(status string) (string, bool) {
	switch status {
	case "zombie", "stopped", "exited":
		return status, false
	case "":
		return status, false
	default:
		return status, true
	}
}
