package cli

import (
	"io"
	"os"
	"path/filepath"
	"time"

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
			Command:  tu.PrettyCommand(),
			Path:     tu.ExtractPath(),
			Category: string(category),
			Status:   status,
			Time:     tu.PrettyTimestamp(),
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
			Command:  tu.PrettyCommand(),
			Path:     tu.ExtractPath(),
			Category: string(category),
			Status:   status,
			Time:     tu.PrettyTimestamp(),
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
