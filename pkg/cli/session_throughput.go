package cli

import (
	"context"
	"os"
	"sort"
	"strings"
	"time"
)

const defaultSessionThroughputLimit = 500

type SessionThroughputOptions struct {
	Source  string
	All     bool
	Project string
	Query   string
	Limit   int
}

type SessionThroughputResult struct {
	Groups  []SessionThroughputGroup `json:"groups"`
	Total   int                      `json:"total"`
	Skipped int                      `json:"skipped"`
	Source  string                   `json:"source"`
	Scope   string                   `json:"scope"`
	Project string                   `json:"project,omitempty"`
}

type SessionThroughputGroup struct {
	Key                    string                   `json:"key"`
	Source                 string                   `json:"source"`
	Model                  string                   `json:"model"`
	ReasoningEffort        string                   `json:"reasoningEffort"`
	Sessions               int                      `json:"sessions"`
	DurationSeconds        float64                  `json:"durationSeconds"`
	InputTokens            int                      `json:"inputTokens,omitempty"`
	OutputTokens           int                      `json:"outputTokens,omitempty"`
	CacheReadTokens        int                      `json:"cacheReadTokens,omitempty"`
	CacheCreationTokens    int                      `json:"cacheCreationTokens,omitempty"`
	TotalTokens            int                      `json:"totalTokens,omitempty"`
	ContextTokens          int                      `json:"contextTokens,omitempty"`
	ContextSamples         int                      `json:"contextSamples,omitempty"`
	OutputTokensPerSecond  float64                  `json:"outputTokensPerSecond"`
	TotalTokensPerSecond   float64                  `json:"totalTokensPerSecond"`
	ContextTokensPerSecond float64                  `json:"contextTokensPerSecond,omitempty"`
	AvgContextUsedPercent  float64                  `json:"avgContextUsedPercent,omitempty"`
	Points                 []SessionThroughputPoint `json:"points,omitempty"`
}

type SessionThroughputPoint struct {
	At                     time.Time `json:"at"`
	SessionID              string    `json:"sessionId"`
	OutputTokensPerSecond  float64   `json:"outputTokensPerSecond"`
	TotalTokensPerSecond   float64   `json:"totalTokensPerSecond"`
	ContextTokensPerSecond float64   `json:"contextTokensPerSecond,omitempty"`
	ContextUsedPercent     float64   `json:"contextUsedPercent,omitempty"`
}

type sessionThroughputAccumulator struct {
	group             SessionThroughputGroup
	contextPercentSum float64
}

func RunSessionThroughput(ctx context.Context, opts SessionThroughputOptions) (SessionThroughputResult, error) {
	source, err := normalizeSessionSource(opts.Source)
	if err != nil {
		return SessionThroughputResult{}, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return SessionThroughputResult{}, err
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultSessionThroughputLimit
	}
	scope, projectRoot, _ := resolveSessionScope(cwd, opts.All, opts.Project)

	db, err := freshenSessionDB(ctx)
	if err != nil {
		return SessionThroughputResult{}, err
	}
	page, err := dbSessionRecords(ctx, db, sessionRecordQuery{
		Source: source, ProjectRoot: projectRoot, Query: opts.Query, Limit: limit,
	})
	if err != nil {
		return SessionThroughputResult{}, err
	}
	return buildSessionThroughputResult(sessionThroughputResultOptions{
		Page: page, Source: source, Scope: scope, Project: projectResultValue(scope, projectRoot),
	}), nil
}

type sessionThroughputResultOptions struct {
	Page    sessionRecordPage
	Source  string
	Scope   string
	Project string
}

func buildSessionThroughputResult(options sessionThroughputResultOptions) SessionThroughputResult {
	groups, skipped := aggregateSessionThroughput(options.Page.Records)
	return SessionThroughputResult{
		Groups: groups, Total: len(options.Page.Records), Skipped: skipped,
		Source: options.Source, Scope: options.Scope, Project: options.Project,
	}
}

func aggregateSessionThroughput(records []SessionRecord) ([]SessionThroughputGroup, int) {
	accs := map[string]*sessionThroughputAccumulator{}
	skipped := 0

	for _, record := range records {
		duration := sessionDurationSeconds(record)
		if duration <= 0 || record.Tokens == nil {
			skipped++
			continue
		}
		totalTokens := record.Tokens.TotalTokens
		if totalTokens == 0 {
			totalTokens = record.Tokens.InputTokens + record.Tokens.OutputTokens + record.Tokens.CacheReadTokens + record.Tokens.CacheCreationTokens
		}
		if totalTokens <= 0 {
			skipped++
			continue
		}

		source := strings.TrimSpace(record.Source)
		if source == "" {
			source = "unknown"
		}
		model := strings.TrimSpace(record.Model)
		if model == "" {
			model = "unknown"
		}
		effort := strings.TrimSpace(record.ReasoningEffort)
		if effort == "" {
			effort = "default"
		}
		key := source + "|" + model + "|" + effort

		acc := accs[key]
		if acc == nil {
			acc = &sessionThroughputAccumulator{group: SessionThroughputGroup{
				Key:             key,
				Source:          source,
				Model:           model,
				ReasoningEffort: effort,
			}}
			accs[key] = acc
		}

		group := &acc.group
		group.Sessions++
		group.DurationSeconds += duration
		group.InputTokens += record.Tokens.InputTokens
		group.OutputTokens += record.Tokens.OutputTokens
		group.CacheReadTokens += record.Tokens.CacheReadTokens
		group.CacheCreationTokens += record.Tokens.CacheCreationTokens
		group.TotalTokens += totalTokens

		point := SessionThroughputPoint{
			At:                    *record.EndedAt,
			SessionID:             record.ID,
			OutputTokensPerSecond: float64(record.Tokens.OutputTokens) / duration,
			TotalTokensPerSecond:  float64(totalTokens) / duration,
		}
		if record.Context != nil && record.Context.UsedTokens > 0 {
			group.ContextTokens += record.Context.UsedTokens
			point.ContextTokensPerSecond = float64(record.Context.UsedTokens) / duration
			if record.Context.WindowTokens > 0 {
				percent := float64(record.Context.UsedTokens) / float64(record.Context.WindowTokens) * 100
				group.ContextSamples++
				acc.contextPercentSum += percent
				point.ContextUsedPercent = percent
			}
		}
		group.Points = append(group.Points, point)
	}

	groups := make([]SessionThroughputGroup, 0, len(accs))
	for _, acc := range accs {
		group := acc.group
		if group.DurationSeconds > 0 {
			group.OutputTokensPerSecond = float64(group.OutputTokens) / group.DurationSeconds
			group.TotalTokensPerSecond = float64(group.TotalTokens) / group.DurationSeconds
			if group.ContextTokens > 0 {
				group.ContextTokensPerSecond = float64(group.ContextTokens) / group.DurationSeconds
			}
		}
		if group.ContextSamples > 0 {
			group.AvgContextUsedPercent = acc.contextPercentSum / float64(group.ContextSamples)
		}
		sort.Slice(group.Points, func(i, j int) bool {
			return group.Points[i].At.Before(group.Points[j].At)
		})
		groups = append(groups, group)
	}

	sort.Slice(groups, func(i, j int) bool {
		if groups[i].OutputTokensPerSecond != groups[j].OutputTokensPerSecond {
			return groups[i].OutputTokensPerSecond > groups[j].OutputTokensPerSecond
		}
		if groups[i].Sessions != groups[j].Sessions {
			return groups[i].Sessions > groups[j].Sessions
		}
		if groups[i].Model != groups[j].Model {
			return groups[i].Model < groups[j].Model
		}
		return groups[i].ReasoningEffort < groups[j].ReasoningEffort
	})
	return groups, skipped
}

func sessionDurationSeconds(record SessionRecord) float64 {
	if record.StartedAt == nil || record.EndedAt == nil || !record.EndedAt.After(*record.StartedAt) {
		return 0
	}
	return record.EndedAt.Sub(*record.StartedAt).Seconds()
}
