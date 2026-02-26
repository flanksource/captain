package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"strings"

	"github.com/flanksource/captain/pkg/bash"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/commons/collections"
)

type HistoryOptions struct {
	File       string    `flag:"file" help:"Read from a JSONL/JSON file instead of session history" short:"f"`
	Tools      []string  `flag:"tool" help:"Filter by tool patterns" short:"t"`
	Dirs       []string  `flag:"dir" help:"Filter by directory patterns" short:"d"`
	Categories []string  `flag:"category" help:"Filter by category patterns" short:"c"`
	Limit      int       `flag:"limit" help:"Maximum results" default:"100" short:"l"`
	Since      time.Time `flag:"since" help:"Only include commands after this time" default:"now-7d" short:"s"`
	All        bool      `flag:"all" help:"Search all projects, not just current directory" short:"a"`
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

	// Apply limit in FilterToolUses only when no category filtering
	if len(opts.Categories) == 0 {
		filter.Limit = opts.Limit
	}

	parseResult, err := claude.ParseHistory(cwd, opts.All, filter)
	if err != nil {
		return nil, err
	}

	scanner := bash.NewScanner(cwd, nil)
	classifier := bash.NewCategoryClassifier(bash.DefaultCategoryConfig())

	costs, _ := claude.ParseCosts(cwd, opts.All, &opts.Since)

	if opts.All {
		return runHistoryAll(parseResult, opts, scanner, classifier, costs)
	}
	return runHistorySingle(parseResult, opts, scanner, classifier, costs)
}

func runHistoryAll(parseResult *claude.ParseResult, opts HistoryOptions, scanner *bash.Scanner, classifier *bash.CategoryClassifier, costs []claude.SessionCost) (any, error) {
	result := HistoryResultAll{
		Results: make([]ScanResultRow, 0, len(parseResult.ToolUses)),
	}

	for _, tu := range parseResult.ToolUses {
		if tu.CWD != "" && tu.ProjectRoot == "" {
			tu.ProjectRoot = claude.FindProjectRoot(tu.CWD)
		}

		category := classifier.ClassifyToolWithPath(tu.Tool, tu.FilePath())
		if category == bash.CategoryOther && tu.Tool == "Bash" {
			if rawCmd, ok := tu.Input["command"].(string); ok {
				category = classifier.ClassifyBash(rawCmd)
			}
		}

		if len(opts.Categories) > 0 && !collections.MatchItems(string(category), opts.Categories...) {
			continue
		}

		cmd := tu.FormatCommand()
		scanResult := scanner.Scan(cmd)
		safe := "✓"
		if !scanResult.Allowed {
			safe = "✗"
			if scanResult.Reason != "" {
				safe += " " + scanResult.Reason
			}
			result.Denied++
		} else {
			result.Allowed++
		}
		result.Total++

		projectName := ""
		if tu.ProjectRoot != "" {
			projectName = filepath.Base(tu.ProjectRoot)
		}

		analysis := AnalyzeToolUse(tu, tu.ProjectRoot)
		row := ScanResultRow{
			Project:         projectName,
			Tool:            tu.DisplayTool(),
			Subject:         tu.PrettyCommand(),
			Paths:           FormatPathsWithIcons(analysis.ReadPaths, analysis.WritePaths),
			ReadPaths:       analysis.ReadPaths,
			WritePaths:      analysis.WritePaths,
			BinariesDisplay: strings.Join(analysis.Binaries, ", "),
			Binaries:        analysis.Binaries,
			Category:        string(category),
			Safe:            safe,
			Time:            tu.PrettyTimestamp(),
		}
		if opts.Debug {
			row.ToolUse = &tu
		}
		result.Results = append(result.Results, row)

		if opts.Limit > 0 && len(result.Results) >= opts.Limit {
			break
		}
	}

	applyCostSummaryAll(&result, costs)
	return result, nil
}

func runHistorySingle(parseResult *claude.ParseResult, opts HistoryOptions, scanner *bash.Scanner, classifier *bash.CategoryClassifier, costs []claude.SessionCost) (any, error) {
	result := HistoryResult{
		Results: make([]ScanResultRowSingle, 0, len(parseResult.ToolUses)),
	}

	for _, tu := range parseResult.ToolUses {
		if tu.CWD != "" && tu.ProjectRoot == "" {
			tu.ProjectRoot = claude.FindProjectRoot(tu.CWD)
		}

		if result.Project == "" && tu.ProjectRoot != "" {
			result.Project = filepath.Base(tu.ProjectRoot)
		}

		category := classifier.ClassifyToolWithPath(tu.Tool, tu.FilePath())
		if category == bash.CategoryOther && tu.Tool == "Bash" {
			if rawCmd, ok := tu.Input["command"].(string); ok {
				category = classifier.ClassifyBash(rawCmd)
			}
		}

		if len(opts.Categories) > 0 && !collections.MatchItems(string(category), opts.Categories...) {
			continue
		}

		cmd := tu.FormatCommand()
		scanResult := scanner.Scan(cmd)
		safe := "✓"
		if !scanResult.Allowed {
			safe = "✗"
			if scanResult.Reason != "" {
				safe += " " + scanResult.Reason
			}
			result.Denied++
		} else {
			result.Allowed++
		}
		result.Total++

		analysis := AnalyzeToolUse(tu, tu.ProjectRoot)
		row := ScanResultRowSingle{
			Tool:            tu.DisplayTool(),
			Subject:         tu.PrettyCommand(),
			Paths:           FormatPathsWithIcons(analysis.ReadPaths, analysis.WritePaths),
			ReadPaths:       analysis.ReadPaths,
			WritePaths:      analysis.WritePaths,
			BinariesDisplay: strings.Join(analysis.Binaries, ", "),
			Binaries:        analysis.Binaries,
			Category:        string(category),
			Safe:            safe,
			Time:            tu.PrettyTimestamp(),
		}
		if opts.Debug {
			row.ToolUse = &tu
		}
		result.Results = append(result.Results, row)

		if opts.Limit > 0 && len(result.Results) >= opts.Limit {
			break
		}
	}

	applyCostSummarySingle(&result, costs)
	return result, nil
}

func applyCostSummaryAll(result *HistoryResultAll, costs []claude.SessionCost) {
	var totalTokens int
	var totalCost float64
	var minStart, maxEnd time.Time

	for _, c := range costs {
		totalTokens += c.Tokens.TotalTokens()
		totalCost += c.Tokens.TotalCost
		if minStart.IsZero() || c.Start.Before(minStart) {
			minStart = c.Start
		}
		if c.End.After(maxEnd) {
			maxEnd = c.End
		}
	}

	result.Tokens = totalTokens
	result.Cost = fmt.Sprintf("$%.4f", totalCost)
	if !minStart.IsZero() && !maxEnd.IsZero() {
		result.Duration = formatDuration(maxEnd.Sub(minStart))
	}
}

func applyCostSummarySingle(result *HistoryResult, costs []claude.SessionCost) {
	var totalTokens int
	var totalCost float64
	var minStart, maxEnd time.Time

	for _, c := range costs {
		totalTokens += c.Tokens.TotalTokens()
		totalCost += c.Tokens.TotalCost
		if minStart.IsZero() || c.Start.Before(minStart) {
			minStart = c.Start
		}
		if c.End.After(maxEnd) {
			maxEnd = c.End
		}
	}

	result.Tokens = totalTokens
	result.Cost = fmt.Sprintf("$%.4f", totalCost)
	if !minStart.IsZero() && !maxEnd.IsZero() {
		result.Duration = formatDuration(maxEnd.Sub(minStart))
	}
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
