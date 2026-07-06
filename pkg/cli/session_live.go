package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/claude"
	rpchttp "github.com/flanksource/clicky/rpc/http"
)

var discoverSessionProcesses = discoverAgentProcesses

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

	scope := "current"
	if opts.All {
		scope = "all"
	}
	limit := opts.Limit
	if limit <= 0 && !opts.Full {
		limit = defaultSessionLiveLimit
	}
	records, err := discoverLiveSessionRecords(ctx, cwd, opts.All, source, limit, opts.Full)
	if err != nil {
		return SessionLiveResult{}, err
	}

	stopEnrich := rpchttp.Track(ctx, "enrich")
	processes, _ := discoverSessionProcesses()
	if !opts.All {
		processes = filterAgentProcessesByProject(processes, sessionProjectRoot(cwd))
	}
	records = enrichSessionsWithLive(records, processes)
	filtered := make([]SessionRecord, 0, len(records))
	for _, record := range records {
		if sessionMatchesQuery(record, opts.Query) {
			filtered = append(filtered, record)
		}
	}
	sortSessionRecords(filtered)
	total := len(filtered)
	summary := summarizeSessionDashboard(filtered)
	stopEnrich()
	if !opts.Full && limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}

	return SessionLiveResult{
		Sessions: filtered,
		Total:    total,
		Source:   source,
		Scope:    scope,
		Summary:  summary,
	}, nil
}

func discoverLiveSessionRecords(ctx context.Context, cwd string, searchAll bool, source string, limit int, full bool) ([]SessionRecord, error) {
	if full {
		list, err := RunSessionList(ctx, SessionListOptions{
			Source: source,
			All:    searchAll,
			Query:  "",
			Limit:  0,
		})
		if err != nil {
			return nil, err
		}
		return list.Sessions, nil
	}

	candidates, err := discoverLiveSessionCandidates(ctx, cwd, searchAll, source, limit)
	if err != nil {
		return nil, err
	}
	records := make([]SessionRecord, 0, len(candidates))
	for _, candidate := range candidates {
		records = append(records, candidate.record)
	}
	sortSessionRecords(records)
	return records, nil
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

func filterAgentProcessesByProject(processes []agentProcess, projectRoot string) []agentProcess {
	if projectRoot == "" {
		return processes
	}
	filtered := make([]agentProcess, 0, len(processes))
	for _, proc := range processes {
		if sessionRecordMatchesProject(SessionRecord{CWD: proc.CWD}, projectRoot) {
			filtered = append(filtered, proc)
		}
	}
	return filtered
}

func enrichSessionsWithLive(records []SessionRecord, processes []agentProcess) []SessionRecord {
	out := make([]SessionRecord, len(records))
	copy(out, records)
	matched := make(map[int]bool)
	for i := range out {
		idx := bestLiveMatch(out[i], processes, matched)
		if idx < 0 {
			out[i].Health = deriveSessionHealth(out[i])
			continue
		}
		matched[idx] = true
		out[i].Live = processes[idx].wire()
		if out[i].CWD == "" {
			out[i].CWD = processes[idx].CWD
		}
		if out[i].StartedAt == nil && processes[idx].StartedAt != nil {
			out[i].StartedAt = processes[idx].StartedAt
		}
		out[i].Health = deriveSessionHealth(out[i])
	}
	for i, proc := range processes {
		if matched[i] {
			continue
		}
		record := SessionRecord{
			Key:             fmt.Sprintf("live-%s-%d", proc.Source, proc.PID),
			ID:              fmt.Sprintf("pid:%d", proc.PID),
			Source:          proc.Source,
			CWD:             proc.CWD,
			StartedAt:       proc.StartedAt,
			DetailAvailable: false,
			Live:            proc.wire(),
		}
		record.Health = deriveSessionHealth(record)
		out = append(out, record)
	}
	sortSessionRecords(out)
	return out
}

func bestLiveMatch(record SessionRecord, processes []agentProcess, matched map[int]bool) int {
	if record.Source == "" {
		return -1
	}
	best := -1
	for i, proc := range processes {
		if matched[i] || proc.Source != record.Source {
			continue
		}
		if record.CWD != "" && proc.CWD != "" && samePath(record.CWD, proc.CWD) {
			return i
		}
		if best < 0 && record.CWD == "" {
			best = i
		}
	}
	return best
}

func samePath(a, b string) bool {
	return strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
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
	}
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
}
