package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/claude"
)

const defaultSessionLiveLimit = 25

func RunSessionLive(ctx context.Context, opts SessionLiveOptions) (SessionLiveResult, error) {
	source, err := normalizeSessionSource(opts.Source)
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
	db, err := freshenSessionDB(ctx)
	if err != nil {
		return SessionLiveResult{}, err
	}
	records, err := dbSessionRecords(ctx, db, sessionRecordQuery{
		Source: source, ProjectRoot: projectRoot, Query: opts.Query,
	})
	if err != nil {
		return SessionLiveResult{}, err
	}
	total := len(records)
	summary := summarizeSessionDashboard(records)
	if !opts.Full && limit > 0 && len(records) > limit {
		records = records[:limit]
	}

	return SessionLiveResult{
		Sessions: records,
		Total:    total,
		Source:   source,
		Scope:    scope,
		Project:  projectResultValue(scope, projectRoot),
		Summary:  summary,
	}, nil
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
