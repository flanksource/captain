package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/google/uuid"
)

type sessionOverviewStore interface {
	ListSessionOverviewsByIdentity(context.Context, string) ([]database.SessionOverview, error)
	ListSessionOverviews(context.Context, database.SessionOverviewFilter) ([]database.SessionOverview, error)
	ListThreadSessionOverviews(context.Context, uuid.UUID) ([]database.SessionOverview, error)
}

// sessionRecordQuery narrows the DB-backed session record list.
type sessionRecordQuery struct {
	Source      string // "all", "claude", or "codex"
	ProjectRoot string
	Query       string
	LiveOnly    bool
}

// dbSessionRecords is the single source for session list/live/throughput
// records: it reads the captain_session_overview view (populated by the
// monitor) and projects rows onto the SessionRecord wire shape.
func dbSessionRecords(ctx context.Context, db sessionOverviewStore, q sessionRecordQuery) ([]SessionRecord, error) {
	filter := database.SessionOverviewFilter{RootsOnly: true, LiveOnly: q.LiveOnly}
	if q.Source != "" && q.Source != "all" {
		filter.Source = q.Source
	}
	var overviews []database.SessionOverview
	var err error
	if isSessionIdentityQuery(q.Query) {
		overviews, err = db.ListSessionOverviewsByIdentity(ctx, q.Query)
		if errors.Is(err, database.ErrSessionNotFound) {
			return []SessionRecord{}, nil
		}
	} else {
		overviews, err = db.ListSessionOverviews(ctx, filter)
	}
	if err != nil {
		return nil, err
	}
	records := make([]SessionRecord, 0, len(overviews))
	for _, overview := range overviews {
		if overview.ParentSessionID != nil ||
			(filter.LiveOnly && !overview.ProcessActive) ||
			(filter.Source != "" && overview.Source != filter.Source) {
			continue
		}
		record := recordFromOverview(overview)
		if q.ProjectRoot != "" && !sessionRecordMatchesProject(record, q.ProjectRoot) {
			continue
		}
		if !sessionMatchesQuery(record, q.Query) {
			continue
		}
		records = append(records, record)
	}
	sortSessionRecords(records)
	return records, nil
}

func isSessionIdentityQuery(query string) bool {
	query = strings.TrimSpace(query)
	if len(query) < 8 || len(query) > 36 {
		return false
	}
	for _, char := range query {
		if char != '-' && (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}

// resolveOverviewsByAnyID resolves sessions by UUID or provider-session-id
// prefix, falling back to the path-derived record Key the UI navigates with.
func resolveOverviewsByAnyID(ctx context.Context, db sessionOverviewStore, id string) ([]database.SessionOverview, error) {
	overviews, err := db.ListSessionOverviewsByIdentity(ctx, id)
	if err == nil {
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
	if !errors.Is(err, database.ErrSessionNotFound) {
		return nil, err
	}
	overviews, listErr := db.ListSessionOverviews(ctx, database.SessionOverviewFilter{RootsOnly: true})
	if listErr != nil {
		return nil, err
	}
	for i := range overviews {
		path := stringOr(overviews[i].HistoryFile, stringOr(overviews[i].Path, ""))
		if path != "" && sessionRecordKey(overviews[i].Source, path) == id {
			return []database.SessionOverview{overviews[i]}, nil
		}
	}
	return nil, err
}

// candidateFromOverview adapts a DB overview row to the transcript-parsing
// candidate shape used by the detail and plan readers.
func candidateFromOverview(overview database.SessionOverview) sessionCandidate {
	path := stringOr(overview.HistoryFile, stringOr(overview.Path, ""))
	return sessionCandidate{
		record: minimalSessionRecord(overview.Source, path, stringOr(overview.ProviderSessionID, overview.ID.String())),
		path:   path,
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

// recordFromOverview projects one overview row to the SessionRecord wire
// shape, keeping the path-derived Key stable with the previous file-scan
// implementation so UI list keys survive the storage switch.
func recordFromOverview(overview database.SessionOverview) SessionRecord {
	metadata := overviewMetadata(overview)
	path := stringOr(overview.HistoryFile, stringOr(overview.Path, ""))
	id := stringOr(overview.ProviderSessionID, overview.ID.String())
	key := overview.ID.String()
	if path != "" {
		key = sessionRecordKey(overview.Source, path)
	}
	record := SessionRecord{
		Key:             key,
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
		Provider:        metadata.Provider,
		Backend:         stringOr(overview.Backend, ""),
		LifecycleStatus: string(overview.LifecycleStatus),
		CWD:             stringOr(overview.CWD, ""),
		ToolCalls:       int(overview.ToolCallCount),
		Messages:        int(overview.MessageCount),
		DetailAvailable: path != "",
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
