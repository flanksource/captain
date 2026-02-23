package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/flanksource/captain/pkg/claude"
)

type CostOptions struct {
	Since   time.Time `flag:"since" help:"Only include sessions after this time" default:"now-7d" short:"s"`
	All     bool      `flag:"all" help:"Search all projects" short:"a"`
	GroupBy string    `flag:"group-by" help:"Group results: session, project, model, day, dir, file" default:"session" short:"g"`
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
	Time       string `json:"time" pretty:"label=Time,table"`
}

type CostResult struct {
	TotalAPICost string    `json:"totalApiCost" pretty:"label=Total API Cost (equivalent)"`
	TotalTokens  string    `json:"totalTokens" pretty:"label=Total Tokens"`
	Rows         []CostRow `json:"rows"`
}

func RunCost(opts CostOptions) (any, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	sessions, err := claude.ParseCosts(cwd, opts.All, &opts.Since)
	if err != nil {
		return nil, err
	}

	grouped := groupSessions(sessions, opts.GroupBy)

	sort.Slice(grouped, func(i, j int) bool {
		return grouped[i].End.After(grouped[j].End)
	})

	var total claude.TokenSummary
	rows := make([]CostRow, 0, len(grouped))
	for _, s := range grouped {
		total.InputTokens += s.Tokens.InputTokens
		total.OutputTokens += s.Tokens.OutputTokens
		total.CacheWriteTokens += s.Tokens.CacheWriteTokens
		total.CacheReadTokens += s.Tokens.CacheReadTokens
		total.TotalCost += s.Tokens.TotalCost

		rows = append(rows, CostRow{
			Project:    s.Project,
			Model:      s.Model,
			Tier:       s.Tier,
			Input:      formatTokens(s.Tokens.InputTokens),
			Output:     formatTokens(s.Tokens.OutputTokens),
			CacheRead:  formatTokens(s.Tokens.CacheReadTokens),
			CacheWrite: formatTokens(s.Tokens.CacheWriteTokens),
			Msgs:       s.Messages,
			APICost:    formatCost(s.Tokens.TotalCost),
			Time:       claude.FormatTimeAgo(&s.End),
		})
	}

	return CostResult{
		TotalAPICost: formatCost(total.TotalCost),
		TotalTokens:  formatTokens(total.TotalTokens()),
		Rows:         rows,
	}, nil
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
				SessionID: s.SessionID,
				Project:   key,
				Model:     s.Model,
				Tier:      s.Tier,
				Start:     s.Start,
				End:       s.End,
				Messages:  s.Messages,
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
	g.Tokens.InputTokens += s.Tokens.InputTokens
	g.Tokens.OutputTokens += s.Tokens.OutputTokens
	g.Tokens.CacheWriteTokens += s.Tokens.CacheWriteTokens
	g.Tokens.CacheReadTokens += s.Tokens.CacheReadTokens
	g.Tokens.TotalCost += s.Tokens.TotalCost
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

func formatTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func formatCost(cost float64) string {
	if cost < 0.01 {
		return fmt.Sprintf("$%.4f", cost)
	}
	return fmt.Sprintf("$%.2f", cost)
}
