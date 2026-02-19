package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/captain/pkg/bash"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/commons/collections"
)

type HistoryOptions struct {
	Tools      []string  `flag:"tool" help:"Filter by tool patterns" short:"t"`
	Dirs       []string  `flag:"dir" help:"Filter by directory patterns" short:"d"`
	Categories []string  `flag:"category" help:"Filter by category patterns" short:"c"`
	Limit      int       `flag:"limit" help:"Maximum results" default:"100" short:"l"`
	Since      time.Time `flag:"since" help:"Only include commands after this time" default:"now-7d" short:"s"`
	All        bool      `flag:"all" help:"Search all projects, not just current directory" short:"a"`
	Debug      bool      `flag:"debug" help:"Include original Claude history struct in results"`
}

func RunHistory(opts HistoryOptions) (any, error) {
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

	if opts.All {
		return runHistoryAll(parseResult, opts, scanner, classifier)
	}
	return runHistorySingle(parseResult, opts, scanner, classifier)
}

func runHistoryAll(parseResult *claude.ParseResult, opts HistoryOptions, scanner *bash.Scanner, classifier *bash.CategoryClassifier) (any, error) {
	result := HistoryResultAll{
		Results: make([]ScanResultRow, 0, len(parseResult.ToolUses)),
	}

	for _, tu := range parseResult.ToolUses {
		if tu.CWD != "" && tu.ProjectRoot == "" {
			tu.ProjectRoot = claude.FindProjectRoot(tu.CWD)
		}

		cmd := tu.FormatCommand()
		category := classifier.ClassifyToolWithPath(tu.Tool, tu.FilePath())
		if category == bash.CategoryOther && tu.Tool == "Bash" {
			if rawCmd, ok := tu.Input["command"].(string); ok {
				category = classifier.ClassifyBash(rawCmd)
			}
		}

		if len(opts.Categories) > 0 && !collections.MatchItems(string(category), opts.Categories...) {
			continue
		}

		scanResult := scanner.Scan(cmd)
		status := "✓"
		if !scanResult.Allowed {
			status = "✗"
			if scanResult.Reason != "" {
				status += " " + scanResult.Reason
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

		row := ScanResultRow{
			Project:  projectName,
			Tool:     tu.Tool,
			Command:  api.NewCode(cmd, toolToLanguage(tu.Tool)),
			Path:     tu.ExtractPath(),
			Category: string(category),
			Status:   status,
			Time:     formatTimeAgo(tu.Timestamp),
		}
		if opts.Debug {
			row.ToolUse = &tu
		}
		result.Results = append(result.Results, row)

		if opts.Limit > 0 && len(result.Results) >= opts.Limit {
			break
		}
	}

	return result, nil
}

func runHistorySingle(parseResult *claude.ParseResult, opts HistoryOptions, scanner *bash.Scanner, classifier *bash.CategoryClassifier) (any, error) {
	result := HistoryResult{
		Results: make([]ScanResultRowSingle, 0, len(parseResult.ToolUses)),
	}

	for _, tu := range parseResult.ToolUses {
		if tu.CWD != "" && tu.ProjectRoot == "" {
			tu.ProjectRoot = claude.FindProjectRoot(tu.CWD)
		}

		// Set project name from first tool use
		if result.Project == "" && tu.ProjectRoot != "" {
			result.Project = filepath.Base(tu.ProjectRoot)
		}

		cmd := tu.FormatCommand()
		category := classifier.ClassifyToolWithPath(tu.Tool, tu.FilePath())
		if category == bash.CategoryOther && tu.Tool == "Bash" {
			if rawCmd, ok := tu.Input["command"].(string); ok {
				category = classifier.ClassifyBash(rawCmd)
			}
		}

		if len(opts.Categories) > 0 && !collections.MatchItems(string(category), opts.Categories...) {
			continue
		}

		scanResult := scanner.Scan(cmd)
		status := "✓"
		if !scanResult.Allowed {
			status = "✗"
			if scanResult.Reason != "" {
				status += " " + scanResult.Reason
			}
			result.Denied++
		} else {
			result.Allowed++
		}
		result.Total++

		row := ScanResultRowSingle{
			Tool:     tu.Tool,
			Command:  api.NewCode(cmd, toolToLanguage(tu.Tool)),
			Path:     tu.ExtractPath(),
			Category: string(category),
			Status:   status,
			Time:     formatTimeAgo(tu.Timestamp),
		}
		if opts.Debug {
			row.ToolUse = &tu
		}
		result.Results = append(result.Results, row)

		if opts.Limit > 0 && len(result.Results) >= opts.Limit {
			break
		}
	}

	return result, nil
}

func formatTimeAgo(t *time.Time) string {
	if t == nil {
		return ""
	}
	d := time.Since(*t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func toolToLanguage(tool string) string {
	switch tool {
	case "Bash":
		return "bash"
	default:
		return ""
	}
}
