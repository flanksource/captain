package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/bash"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/claude/tools"
	"github.com/flanksource/clicky/api"
)

type HistoryOptions struct {
	File       string    `flag:"file" help:"Read from a JSONL/JSON file instead of session history" short:"f"`
	Tools      []string  `flag:"tool" help:"Filter by tool patterns" short:"t"`
	Dirs       []string  `flag:"dir" help:"Filter by directory patterns" short:"d"`
	Categories []string  `flag:"category" help:"Filter by category patterns" short:"c"`
	Approved   string    `flag:"approved" help:"Filter by approval status (true=approved, false=denied)"`
	Limit      int       `flag:"limit" help:"Maximum results" default:"100" short:"l"`
	Since      time.Time `flag:"since" help:"Only include commands after this time" default:"now-7d" short:"s"`
	All        bool      `flag:"all" help:"Search all projects, not just current directory" short:"a"`
	Short      bool      `flag:"short" help:"Compact output without diffs and code blocks" short:"S"`
	Compact    bool      `flag:"compact" help:"Single line per entry" short:"C"`
	Summary    bool      `flag:"summary" help:"Show aggregate summary instead of individual tool uses"`
	Debug      bool      `flag:"debug" help:"Include original Claude history struct in results"`
}

func RunHistory(opts HistoryOptions) (any, error) {
	if opts.File != "" {
		data, err := os.ReadFile(opts.File)
		if err != nil {
			return nil, err
		}
		return runHistoryFromReader(data, opts)
	}

	if claude.IsStdinPiped() {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, err
		}
		return runHistoryFromReader(data, opts)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	filter := claude.Filter{
		Tools: opts.Tools,
		Dirs:  opts.Dirs,
		Since: &opts.Since,
	}

	if len(opts.Categories) == 0 {
		filter.Limit = opts.Limit
	}

	parseResult, err := claude.ParseHistory(cwd, opts.All, filter)
	if err != nil {
		return nil, err
	}

	classifier := bash.NewCategoryClassifier(bash.DefaultCategoryConfig())

	var costs []claude.SessionCost
	if opts.Summary {
		costs, _ = claude.ParseCostsDetailed(cwd, opts.All, &opts.Since)
		return runHistorySummary(parseResult.ToolUses, opts, classifier, costs)
	}

	costs, _ = claude.ParseCosts(cwd, opts.All, &opts.Since)
	// Convert ToolUses to Tool interface
	tl := claude.ToolUsesToTools(parseResult.ToolUses)
	if opts.All {
		return runHistoryAll(tl, opts, classifier, costs)
	}
	return runHistorySingle(tl, opts, classifier, costs)
}

func runHistoryAll(tl []tools.Tool, opts HistoryOptions, classifier *bash.CategoryClassifier, costs []claude.SessionCost) (any, error) {
	filtered := filterTools(tl, opts, classifier)

	if !useStructuredOutput() {
		renderLineByLine(filtered, opts.Compact || opts.Short)
		return nil, nil
	}

	result := HistoryResultAll{
		Results: make([]ScanResultRow, 0, len(filtered)),
	}

	for _, t := range filtered {
		base := t.Base()
		result.Total++

		approved := approvedStatus(t)
		if base.Denied {
			result.UserDenied++
		}

		projectName := ""
		if base.ProjectRoot != "" {
			projectName = filepath.Base(base.ProjectRoot)
		}

		analysis := AnalyzeToolUse(t)
		row := ScanResultRow{
			Project:         projectName,
			Tool:            t.Name(),
			Summary:         firstLine(t.Pretty().String()),
			Subject:         t.Pretty(),
			Detail:          t.Detail(),
			Paths:           FormatPathsWithIcons(analysis.ReadPaths, analysis.WritePaths),
			ReadPaths:       analysis.ReadPaths,
			WritePaths:      analysis.WritePaths,
			BinariesDisplay: strings.Join(analysis.Binaries, ", "),
			Binaries:        analysis.Binaries,
			Approved:        approved,
			Time:            base.PrettyTimestamp(),
		}
		if !opts.Compact {
			row.Category = classifyTool(t, classifier)
		}
		result.Results = append(result.Results, row)
	}

	applyCostSummaryAll(&result, costs)
	if useTableOutput() {
		return api.NewTableFrom(result.Results), nil
	}
	return result, nil
}

func runHistorySingle(tl []tools.Tool, opts HistoryOptions, classifier *bash.CategoryClassifier, costs []claude.SessionCost) (any, error) {
	filtered := filterTools(tl, opts, classifier)

	if !useStructuredOutput() {
		renderLineByLine(filtered, opts.Compact || opts.Short)
		return nil, nil
	}

	result := HistoryResult{
		Results: make([]ScanResultRowSingle, 0, len(filtered)),
	}

	for _, t := range filtered {
		base := t.Base()
		if result.Project == "" && base.ProjectRoot != "" {
			result.Project = filepath.Base(base.ProjectRoot)
		}

		result.Total++

		approved := approvedStatus(t)
		if base.Denied {
			result.UserDenied++
		}

		analysis := AnalyzeToolUse(t)
		row := ScanResultRowSingle{
			Tool:            t.Name(),
			Summary:         firstLine(t.Pretty().String()),
			Subject:         t.Pretty(),
			Detail:          t.Detail(),
			Paths:           FormatPathsWithIcons(analysis.ReadPaths, analysis.WritePaths),
			ReadPaths:       analysis.ReadPaths,
			WritePaths:      analysis.WritePaths,
			BinariesDisplay: strings.Join(analysis.Binaries, ", "),
			Binaries:        analysis.Binaries,
			Approved:        approved,
			Time:            base.PrettyTimestamp(),
		}
		if !opts.Compact {
			row.Category = classifyTool(t, classifier)
		}
		result.Results = append(result.Results, row)
	}

	applyCostSummarySingle(&result, costs)
	if useTableOutput() {
		return api.NewTableFrom(result.Results), nil
	}
	return result, nil
}

func filterTools(tl []tools.Tool, opts HistoryOptions, classifier *bash.CategoryClassifier) []tools.Tool {
	var result []tools.Tool
	for _, t := range tl {
		base := t.Base()

		cat := classifyTool(t, classifier)
		if len(opts.Categories) > 0 && !claude.MatchItemsInsensitive(cat, opts.Categories) {
			continue
		}
		if !matchApprovedFilter(opts.Approved, base.Denied) {
			continue
		}

		result = append(result, t)
		if opts.Limit > 0 && len(result) >= opts.Limit {
			break
		}
	}
	return result
}

func classifyTool(t tools.Tool, classifier *bash.CategoryClassifier) string {
	if cat := t.Category(); cat != "" {
		return cat
	}
	base := t.Base()
	cat := classifier.ClassifyToolWithPath(base.RawTool, t.FilePath())
	if cat == bash.CategoryOther && base.RawTool == "Bash" {
		if rawCmd, ok := base.Input["command"].(string); ok {
			cat = classifier.ClassifyBash(rawCmd)
		}
	}
	return string(cat)
}

func approvedStatus(t tools.Tool) string {
	base := t.Base()
	name := t.Name()
	if base.RawTool == "ExitPlanMode" || base.RawTool == "User" || name == "Plan" {
		return ""
	}
	if base.Denied {
		return "✗"
	}
	return "✓"
}

func matchApprovedFilter(filter string, denied bool) bool {
	switch filter {
	case "true":
		return !denied
	case "false":
		return denied
	default:
		return true
	}
}

func applyCostSummaryAll(result *HistoryResultAll, costs []claude.SessionCost) {
	var totals claude.TokenSummary
	var minStart, maxEnd time.Time

	for _, c := range costs {
		totals.InputTokens += c.Tokens.InputTokens
		totals.OutputTokens += c.Tokens.OutputTokens
		totals.CacheReadTokens += c.Tokens.CacheReadTokens
		totals.CacheWriteTokens += c.Tokens.CacheWriteTokens
		totals.TotalCost += c.Tokens.TotalCost
		if minStart.IsZero() || c.Start.Before(minStart) {
			minStart = c.Start
		}
		if c.End.After(maxEnd) {
			maxEnd = c.End
		}
	}

	result.Tokens = totals.TotalTokens()
	result.InputTokens = formatTokens(totals.InputTokens)
	result.OutputTokens = formatTokens(totals.OutputTokens)
	result.CacheRead = formatTokens(totals.CacheReadTokens)
	result.CacheWrite = formatTokens(totals.CacheWriteTokens)
	result.Cost = formatCost(totals.TotalCost)
	if !minStart.IsZero() && !maxEnd.IsZero() {
		result.Duration = formatDuration(maxEnd.Sub(minStart))
	}
}

func applyCostSummarySingle(result *HistoryResult, costs []claude.SessionCost) {
	var totals claude.TokenSummary
	var minStart, maxEnd time.Time

	for _, c := range costs {
		totals.InputTokens += c.Tokens.InputTokens
		totals.OutputTokens += c.Tokens.OutputTokens
		totals.CacheReadTokens += c.Tokens.CacheReadTokens
		totals.CacheWriteTokens += c.Tokens.CacheWriteTokens
		totals.TotalCost += c.Tokens.TotalCost
		if minStart.IsZero() || c.Start.Before(minStart) {
			minStart = c.Start
		}
		if c.End.After(maxEnd) {
			maxEnd = c.End
		}
	}

	result.Tokens = totals.TotalTokens()
	result.InputTokens = formatTokens(totals.InputTokens)
	result.OutputTokens = formatTokens(totals.OutputTokens)
	result.CacheRead = formatTokens(totals.CacheReadTokens)
	result.CacheWrite = formatTokens(totals.CacheWriteTokens)
	result.Cost = formatCost(totals.TotalCost)
	if !minStart.IsZero() && !maxEnd.IsZero() {
		result.Duration = formatDuration(maxEnd.Sub(minStart))
	}
}

func runHistorySummary(toolUses []claude.ToolUse, opts HistoryOptions, classifier *bash.CategoryClassifier, costs []claude.SessionCost) (any, error) {
	var filtered []claude.ToolUse
	for _, tu := range toolUses {
		if !matchApprovedFilter(opts.Approved, tu.Denied) {
			continue
		}
		if len(opts.Categories) > 0 {
			category := classifier.ClassifyToolWithPath(tu.Tool, tu.FilePath())
			if category == bash.CategoryOther && tu.Tool == "Bash" {
				if rawCmd, ok := tu.Input["command"].(string); ok {
					category = classifier.ClassifyBash(rawCmd)
				}
			}
			if !claude.MatchItemsInsensitive(string(category), opts.Categories) {
				continue
			}
		}
		filtered = append(filtered, tu)
	}
	return BuildSummary(filtered, classifier, costs), nil
}

func formatDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
