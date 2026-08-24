package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/session"
)

const (
	costSourceCaptain   = "captain_estimate"
	costSourceClaudeCLI = "claude_cli_estimate"
	costSourceProvider  = "provider"
	costSourceMixed     = "mixed"
	costSourceAllocated = "allocated"
)

type CostOptions struct {
	Since     time.Time `flag:"since" help:"Only include sessions after this time" default:"now-7d" short:"s"`
	All       bool      `flag:"all" help:"Search all projects" short:"a"`
	GroupBy   string    `flag:"group-by" help:"Group results: session, project, model, day, dir, file, tool, category" default:"session" short:"g"`
	SessionID string    `flag:"session-id" help:"Filter by session ID (exact or prefix match)"`
}

type CostRow struct {
	Project    string `json:"project" pretty:"label=Project,table"`
	Model      string `json:"model" pretty:"label=Model,table"`
	Tier       string `json:"tier" pretty:"label=Tier,table"`
	Input      string `json:"input" pretty:"label=Input,table"`
	Output     string `json:"output" pretty:"label=Output,table"`
	CacheRead  string `json:"cacheRead" pretty:"label=Cache Read,table"`
	CacheWrite string `json:"cacheWrite" pretty:"label=Cache Write,table"`
	Msgs       int    `json:"msgs" pretty:"label=Msgs,table"`
	APICost    string `json:"apiCost" pretty:"label=API Cost,table"`
	CostBasis  string `json:"costBasis" pretty:"label=Cost Basis,table"`
	Time       string `json:"time" pretty:"label=Time,table"`
}

type CostResult struct {
	TotalAPICost   string    `json:"totalApiCost" pretty:"label=Total API Cost (equivalent)"`
	TotalCostBasis string    `json:"totalCostBasis" pretty:"label=Total Cost Basis"`
	TotalTokens    string    `json:"totalTokens" pretty:"label=Total Tokens"`
	Rows           []CostRow `json:"rows"`
}

type ToolCostRow struct {
	Tool   string `json:"tool" pretty:"label=Tool,table"`
	Calls  int    `json:"calls" pretty:"label=Calls,table"`
	Input  string `json:"input" pretty:"label=Input (est),table"`
	Output string `json:"output" pretty:"label=Output (est),table"`
	Errors int    `json:"errors" pretty:"label=Errors,table"`
}

type ToolCostResult struct {
	TotalTokens string        `json:"totalTokens" pretty:"label=Total Estimated Tokens"`
	Rows        []ToolCostRow `json:"rows"`
}

type CategoryCostRow struct {
	Category string `json:"category" pretty:"label=Category,table"`
	Tokens   string `json:"tokens" pretty:"label=Tokens (est),table"`
	Percent  string `json:"percent" pretty:"label=%,table"`
}

type CategoryCostResult struct {
	TotalTokens string            `json:"totalTokens" pretty:"label=Total Estimated Tokens"`
	Rows        []CategoryCostRow `json:"rows"`
}

func RunCost(ctx context.Context, opts CostOptions) (any, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	if opts.GroupBy == "tool" || opts.GroupBy == "category" {
		return runCostDetailed(cwd, opts)
	}

	sessionIDs := costSessionIDs(opts)
	sessions, err := claude.ParseCostsWithFilter(cwd, opts.All, &opts.Since, claude.Filter{SessionIDs: sessionIDs})
	if err != nil {
		return nil, err
	}
	sessions = filterCostsBySessionID(sessions, sessionIDs)
	// Transcripts hold no result total, so prefer a complete provider-attributed
	// thread and then a positive Claude CLI estimate Captain observed.
	applyResultCosts(ctx, sessions)

	grouped := groupSessions(sessions, opts.GroupBy)

	sort.Slice(grouped, func(i, j int) bool {
		return grouped[i].End.After(grouped[j].End)
	})

	var total claude.TokenSummary
	var totalSource string
	rows := make([]CostRow, 0, len(grouped))
	for _, s := range grouped {
		source := sessionCostSource(s)
		totalSource = mergeCostSource(totalSource, source)
		total.InputTokens += s.Tokens.InputTokens
		total.OutputTokens += s.Tokens.OutputTokens
		total.CacheWriteTokens += s.Tokens.CacheWriteTokens
		total.CacheReadTokens += s.Tokens.CacheReadTokens
		total.TotalCost += s.Tokens.TotalCost
		total.ProviderCostUSD += s.Tokens.ProviderCostUSD

		rows = append(rows, CostRow{
			Project:    s.Project,
			Model:      s.Model,
			Tier:       s.Tier,
			Input:      session.FormatTokens(s.Tokens.InputTokens),
			Output:     session.FormatTokens(s.Tokens.OutputTokens),
			CacheRead:  session.FormatTokens(s.Tokens.CacheReadTokens),
			CacheWrite: session.FormatTokens(s.Tokens.CacheWriteTokens),
			Msgs:       s.Messages,
			APICost:    session.FormatCostEstimated(s.Tokens.TotalCost, costSourceEstimated(source)),
			CostBasis:  costSourceLabel(source),
			Time:       claude.FormatTimeAgo(&s.End),
		})
	}

	return CostResult{
		TotalAPICost:   session.FormatCostEstimated(total.TotalCost, costSourceEstimated(totalSource)),
		TotalCostBasis: costSourceLabel(totalSource),
		TotalTokens:    session.FormatTokens(total.TotalTokens()),
		Rows:           rows,
	}, nil
}

func runCostDetailed(cwd string, opts CostOptions) (any, error) {
	sessionIDs := costSessionIDs(opts)
	sessions, err := claude.ParseCostsDetailedWithFilter(cwd, opts.All, &opts.Since, claude.Filter{SessionIDs: sessionIDs})
	if err != nil {
		return nil, err
	}
	sessions = filterCostsBySessionID(sessions, sessionIDs)

	if opts.GroupBy == "tool" {
		return aggregateToolCosts(sessions), nil
	}
	return aggregateCategoryCosts(sessions), nil
}

func costSessionIDs(opts CostOptions) []string {
	ids, _ := normalizeSessionIDFilters("", opts.SessionID, nil)
	return ids
}

func aggregateToolCosts(sessions []claude.SessionCost) ToolCostResult {
	merged := make(map[string]*claude.ToolTokenSummary)
	for _, s := range sessions {
		for _, tc := range s.ToolCosts {
			m, ok := merged[tc.Tool]
			if !ok {
				cp := tc
				merged[tc.Tool] = &cp
				continue
			}
			m.CallCount += tc.CallCount
			m.InputTokens += tc.InputTokens
			m.OutputTokens += tc.OutputTokens
			m.ErrorCount += tc.ErrorCount
		}
	}

	var rows []ToolCostRow
	var total int
	for _, m := range merged {
		total += m.TotalTokens()
		rows = append(rows, ToolCostRow{
			Tool:   m.Tool,
			Calls:  m.CallCount,
			Input:  session.FormatTokens(m.InputTokens),
			Output: session.FormatTokens(m.OutputTokens),
			Errors: m.ErrorCount,
		})
	}

	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Calls > rows[j].Calls
	})

	return ToolCostResult{
		TotalTokens: session.FormatTokens(total),
		Rows:        rows,
	}
}

func aggregateCategoryCosts(sessions []claude.SessionCost) CategoryCostResult {
	totals := make(map[claude.ContentCategory]int)
	var grand int
	for _, s := range sessions {
		if s.Context == nil {
			continue
		}
		for cat, tokens := range s.Context.Categories {
			totals[cat] += tokens
			grand += tokens
		}
	}

	var rows []CategoryCostRow
	for cat, tokens := range totals {
		pct := 0.0
		if grand > 0 {
			pct = float64(tokens) / float64(grand) * 100
		}
		rows = append(rows, CategoryCostRow{
			Category: string(cat),
			Tokens:   session.FormatTokens(tokens),
			Percent:  fmt.Sprintf("%.1f%%", pct),
		})
	}

	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Category < rows[j].Category
	})

	return CategoryCostResult{
		TotalTokens: session.FormatTokens(grand),
		Rows:        rows,
	}
}

func groupSessions(sessions []claude.SessionCost, groupBy string) []claude.SessionCost {
	if groupBy == "session" {
		return sessions
	}

	if groupBy == "dir" || groupBy == "file" {
		return groupByPath(sessions, groupBy)
	}

	type groupKey string
	groups := make(map[groupKey]*claude.SessionCost)
	var order []groupKey

	for _, s := range sessions {
		var key groupKey
		switch groupBy {
		case "project":
			key = groupKey(s.Project)
		case "model":
			key = groupKey(s.Model)
		case "day":
			key = groupKey(s.Start.Format("2006-01-02"))
		default:
			key = groupKey(s.SessionID)
		}

		g, ok := groups[key]
		if !ok {
			cp := s
			if groupBy == "day" {
				cp.Project = s.Start.Format("2006-01-02")
			}
			groups[key] = &cp
			order = append(order, key)
			continue
		}

		mergeInto(g, s)
	}

	result := make([]claude.SessionCost, 0, len(order))
	for _, key := range order {
		result = append(result, *groups[key])
	}
	return result
}

// groupByPath splits each session's cost evenly across its touched files/dirs.
func groupByPath(sessions []claude.SessionCost, mode string) []claude.SessionCost {
	groups := make(map[string]*claude.SessionCost)
	var order []string

	for _, s := range sessions {
		keys := uniquePaths(s.Files, mode)
		if len(keys) == 0 {
			continue
		}
		fraction := 1.0 / float64(len(keys))

		for _, key := range keys {
			split := claude.SessionCost{
				SessionID:  s.SessionID,
				Project:    key,
				Model:      s.Model,
				Tier:       s.Tier,
				Start:      s.Start,
				End:        s.End,
				Messages:   s.Messages,
				CostSource: costSourceAllocated,
				Tokens: claude.TokenSummary{
					InputTokens:      int(float64(s.Tokens.InputTokens) * fraction),
					OutputTokens:     int(float64(s.Tokens.OutputTokens) * fraction),
					CacheWriteTokens: int(float64(s.Tokens.CacheWriteTokens) * fraction),
					CacheReadTokens:  int(float64(s.Tokens.CacheReadTokens) * fraction),
					TotalCost:        s.Tokens.TotalCost * fraction,
				},
			}

			g, ok := groups[key]
			if !ok {
				groups[key] = &split
				order = append(order, key)
				continue
			}
			mergeInto(g, split)
		}
	}

	result := make([]claude.SessionCost, 0, len(order))
	for _, key := range order {
		result = append(result, *groups[key])
	}
	return result
}

func uniquePaths(files []string, mode string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, f := range files {
		key := f
		if mode == "dir" {
			key = filepath.Dir(f)
		}
		if !seen[key] {
			seen[key] = true
			result = append(result, key)
		}
	}
	return result
}

func mergeInto(g *claude.SessionCost, s claude.SessionCost) {
	g.CostSource = mergeCostSource(sessionCostSource(*g), sessionCostSource(s))
	g.Tokens.InputTokens += s.Tokens.InputTokens
	g.Tokens.OutputTokens += s.Tokens.OutputTokens
	g.Tokens.CacheWriteTokens += s.Tokens.CacheWriteTokens
	g.Tokens.CacheReadTokens += s.Tokens.CacheReadTokens
	g.Tokens.TotalCost += s.Tokens.TotalCost
	g.Tokens.ProviderCostUSD += s.Tokens.ProviderCostUSD
	g.Messages += s.Messages
	if s.Start.Before(g.Start) {
		g.Start = s.Start
	}
	if s.End.After(g.End) {
		g.End = s.End
	}
	if g.Model != s.Model {
		g.Model = "mixed"
	}
	if s.Tier != "" {
		g.Tier = s.Tier
	}
}

func sessionCostSource(cost claude.SessionCost) string {
	if cost.CostSource != "" {
		return cost.CostSource
	}
	if cost.Tokens.ProviderCostUSD > 0 {
		return costSourceProvider
	}
	return costSourceCaptain
}

func mergeCostSource(current, next string) string {
	if current == "" || current == next {
		return next
	}
	return costSourceMixed
}

func costSourceEstimated(source string) bool { return source != costSourceProvider }

func costSourceLabel(source string) string {
	switch source {
	case costSourceClaudeCLI:
		return "Claude CLI estimate"
	case costSourceProvider:
		return "Provider reported"
	case costSourceMixed:
		return "Mixed cost sources"
	case costSourceAllocated:
		return "Allocated session estimate"
	default:
		return "Captain list-price estimate"
	}
}
