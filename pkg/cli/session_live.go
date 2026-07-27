package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/database"
)

const defaultSessionLiveLimit = 25

type SessionDatabaseStatusWire struct {
	Source              string     `json:"source,omitempty"`
	DSN                 string     `json:"dsn,omitempty"`
	Coverage            string     `json:"coverage"`
	ReadAt              time.Time  `json:"readAt"`
	LatestSampledAt     *time.Time `json:"latestSampledAt,omitempty"`
	LatestHeartbeatAt   *time.Time `json:"latestHeartbeatAt,omitempty"`
	EarliestLeaseExpiry *time.Time `json:"earliestLeaseExpiry,omitempty"`
}

func RunSessionLive(ctx context.Context, opts SessionLiveOptions) (SessionLiveResult, error) {
	source, err := normalizeSessionSource(opts.Source)
	if err != nil {
		return SessionLiveResult{}, err
	}
	activityFrom, activityBefore, err := sessionActivityRange(opts.From, opts.Before)
	if err != nil {
		return SessionLiveResult{}, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return SessionLiveResult{}, err
	}

	scope, projectRoot, _ := resolveSessionScope(cwd, opts.All, opts.Project)
	limit := opts.Limit
	if limit <= 0 && !opts.Full {
		limit = defaultSessionLiveLimit
	}
	db, err := captainDB(ctx)
	if err != nil {
		return SessionLiveResult{}, err
	}
	query := sessionRecordQuery{
		Source: source, ProjectRoot: projectRoot, Query: opts.Query, LiveOnly: true,
		Limit: limit, Cursor: opts.Cursor, ActivityFrom: activityFrom, ActivityBefore: activityBefore,
	}
	var page sessionRecordPage
	if opts.Full {
		page, err = dbAllSessionRecords(ctx, db, query)
	} else {
		page, err = dbSessionRecords(ctx, db, query)
	}
	if err != nil {
		return SessionLiveResult{}, err
	}
	enrichLiveSessionSurfaces(page.Records)
	coverage := "page"
	if opts.Full {
		coverage = "all"
	}
	return buildSessionLiveResult(sessionLiveResultOptions{
		Page: page, Source: source, Scope: scope, Project: projectResultValue(scope, projectRoot),
		ReadAt: time.Now().UTC(), DatabaseCoverage: coverage,
	}), nil
}

type sessionLiveResultOptions struct {
	Page             sessionRecordPage
	Source           string
	Scope            string
	Project          string
	ReadAt           time.Time
	DatabaseCoverage string
}

func buildSessionLiveResult(options sessionLiveResultOptions) SessionLiveResult {
	coverage := options.DatabaseCoverage
	if coverage == "" {
		coverage = "page"
	}
	databaseStatus := sessionDatabaseStatus(options.Page.Records, options.ReadAt)
	databaseStatus.Coverage = coverage
	return SessionLiveResult{
		Sessions: options.Page.Records, Total: options.Page.Total, Source: options.Source,
		Scope: options.Scope, Project: options.Project,
		Summary: summarizeSessionDashboard(options.Page.Records), Database: databaseStatus,
		NextCursor: options.Page.NextCursor,
	}
}

func enrichLiveSessionSurfaces(records []SessionRecord) {
	detected := false
	for i := range records {
		if records[i].Live == nil || records[i].Live.PID <= 0 {
			continue
		}
		if records[i].Live.Surface != nil {
			detected = true
		}
		if surface := discoverProcessSurface(records[i].Live.PID); surface != nil {
			records[i].Live.Surface = surface
			detected = true
		}
	}
	if !detected {
		return
	}
	surfaces, err := discoverCmuxSurfaces()
	if err != nil {
		return
	}
	for i := range records {
		if records[i].Live == nil {
			continue
		}
		if surface := records[i].Live.Surface; surface != nil && surface.SurfaceID != "" {
			enrichCmuxSurface(surface, surfaces)
		}
	}
}

func sessionDatabaseStatus(records []SessionRecord, readAt time.Time) SessionDatabaseStatusWire {
	dsn, source := captainDatabaseIdentity()
	status := SessionDatabaseStatusWire{Source: source, DSN: database.MaskDSN(dsn), ReadAt: readAt}
	for _, record := range records {
		if record.Live == nil {
			continue
		}
		if sampledAt := record.Live.SampledAt; sampledAt != nil &&
			(status.LatestSampledAt == nil || sampledAt.After(*status.LatestSampledAt)) {
			status.LatestSampledAt = sampledAt
		}
		if heartbeatAt := record.Live.LastHeartbeatAt; heartbeatAt != nil &&
			(status.LatestHeartbeatAt == nil || heartbeatAt.After(*status.LatestHeartbeatAt)) {
			status.LatestHeartbeatAt = heartbeatAt
		}
		if expiresAt := record.Live.LeaseExpiresAt; expiresAt != nil &&
			(status.EarliestLeaseExpiry == nil || expiresAt.Before(*status.EarliestLeaseExpiry)) {
			status.EarliestLeaseExpiry = expiresAt
		}
	}
	return status
}

func sessionProjectRoot(cwd string) string {
	if cwd == "" {
		return ""
	}
	projectInfo := claude.FindProjectInfo(cwd)
	if projectInfo.Root != "" {
		return projectInfo.Root
	}
	return cwd
}

func deriveSessionHealth(record SessionRecord) []SessionHealthWire {
	var health []SessionHealthWire
	if record.Context != nil {
		switch {
		case record.Context.FreePercent < 8:
			health = append(health, SessionHealthWire{
				Kind:     "low_context",
				Severity: "critical",
				Message:  "Context below 8% free",
			})
		case record.Context.FreePercent < 15:
			health = append(health, SessionHealthWire{
				Kind:     "low_context",
				Severity: "warning",
				Message:  "Context below 15% free",
			})
		}
	}
	if record.CostUSD >= 5 {
		health = append(health, SessionHealthWire{
			Kind:     "cost_spike",
			Severity: "warning",
			Message:  "Estimated session cost is above $5",
		})
	}
	if record.Live != nil {
		switch strings.ToLower(record.Live.Status) {
		case "zombie":
			health = append(health, SessionHealthWire{
				Kind:     "zombie",
				Severity: "critical",
				Message:  "Process is a zombie",
			})
		case "stopped":
			health = append(health, SessionHealthWire{
				Kind:     "stopped",
				Severity: "warning",
				Message:  "Process is stopped",
			})
		}
		if record.Live.Active && record.EndedAt != nil && time.Since(*record.EndedAt) > 10*time.Minute {
			health = append(health, SessionHealthWire{
				Kind:     "idle",
				Severity: "warning",
				Message:  "Live process has no recent session activity",
			})
		}
	}
	return health
}

func summarizeSessionDashboard(records []SessionRecord) SessionDashboardWire {
	summary := SessionDashboardWire{TotalSessions: len(records)}
	lowestSet := false
	for _, record := range records {
		if record.Live != nil {
			summary.LiveSessions++
			if record.Live.Active {
				summary.ActiveSessions++
			}
			switch strings.ToLower(record.Live.Status) {
			case "stopped", "zombie":
				summary.StoppedSessions++
			}
		}
		if len(record.Health) > 0 {
			summary.AlertSessions++
		}
		if record.Tokens != nil {
			summary.InputTokens += record.Tokens.InputTokens
			summary.OutputTokens += record.Tokens.OutputTokens
			summary.CacheReadTokens += record.Tokens.CacheReadTokens
			summary.CacheCreationTokens += record.Tokens.CacheCreationTokens
			summary.TotalTokens += record.Tokens.TotalTokens
		}
		summary.CostUSD += record.CostUSD
		if record.Context != nil {
			if !lowestSet || record.Context.FreePercent < *summary.LowestContextFree {
				value := record.Context.FreePercent
				summary.LowestContextFree = &value
				lowestSet = true
			}
		}
	}
	if summary.TotalTokens == 0 {
		summary.TotalTokens = summary.InputTokens + summary.OutputTokens + summary.CacheReadTokens + summary.CacheCreationTokens
	}
	return summary
}

func healthMatchesQuery(health []SessionHealthWire, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	for _, signal := range health {
		if strings.Contains(strings.ToLower(signal.Kind), query) ||
			strings.Contains(strings.ToLower(signal.Severity), query) ||
			strings.Contains(strings.ToLower(signal.Message), query) {
			return true
		}
	}
	return false
}

func liveMatchesQuery(live *SessionLiveWire, query string) bool {
	if live == nil {
		return false
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	values := []string{
		fmt.Sprintf("%d", live.PID),
		live.Status,
		live.CWD,
		live.Command,
		live.SessionID,
	}
	values = append(values, live.AgentIDs...)
	if live.Surface != nil {
		values = append(values, live.Surface.SurfaceID, live.Surface.TabID, live.Surface.WorkspaceID, live.Surface.AgentKind)
	}
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
}
