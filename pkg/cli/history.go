package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/bash"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/claude/tools"
	captainCollections "github.com/flanksource/captain/pkg/collections"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/commons/collections"
)

type HistoryOptions struct {
	Paths      []string  `args:"true" help:"Filter by file or directory paths"`
	File       string    `flag:"file" help:"Read from a JSONL/JSON file ('-' for stdin) instead of session history" short:"f"`
	Tools      []string  `flag:"tool" help:"Filter by tool patterns" short:"t"`
	Categories []string  `flag:"category" help:"Filter by category patterns" short:"c"`
	Approved   string    `flag:"approved" help:"Filter by approval status (true=approved, false=denied)"`
	Limit      int       `flag:"limit" help:"Maximum results" default:"100" short:"l"`
	Since      time.Time `flag:"since" help:"Only include commands after this time" default:"now-7d" short:"s"`
	All        bool      `flag:"all" help:"Search all projects, not just current directory" short:"a"`
	Claude     bool      `flag:"claude" help:"Show only Claude history"`
	Codex      bool      `flag:"codex" help:"Show only Codex history"`
	Short      bool      `flag:"short" help:"Compact output without diffs and code blocks" short:"S"`
	Compact    bool      `flag:"compact" help:"Single line per entry" short:"C"`
	Summary    bool      `flag:"summary" help:"Show aggregate summary instead of individual tool uses"`
	Cost       bool      `flag:"cost" help:"Include per-row token breakdown and dollar cost in row detail"`
	Raw        bool      `flag:"raw" help:"Include the raw Claude session JSONL line in row detail"`
	Debug      bool      `flag:"debug" help:"Include original Claude history struct in results"`
}

func RunHistory(opts HistoryOptions) (any, error) {
	if opts.File == "-" || opts.File == "/dev/stdin" || (opts.File == "" && claude.IsStdinPiped()) {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, err
		}
		claude.ResetUnhandledStreamTypes()
		out, err := runHistoryFromReader(data, opts)
		reportUnhandledStreamTypes()
		return out, err
	}

	if opts.File != "" {
		data, err := os.ReadFile(opts.File)
		if err != nil {
			return nil, err
		}
		claude.ResetUnhandledStreamTypes()
		out, err := runHistoryFromReader(data, opts)
		reportUnhandledStreamTypes()
		return out, err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	filter := claude.Filter{
		Tools: opts.Tools,
		Paths: resolvePaths(opts.Paths),
		Since: &opts.Since,
	}

	if len(opts.Categories) == 0 {
		filter.Limit = opts.Limit
	}

	showClaude := opts.Claude || (!opts.Claude && !opts.Codex)
	showCodex := opts.Codex || (!opts.Claude && !opts.Codex)

	var allToolUses []claude.ToolUse

	if showClaude {
		parseResult, err := claude.ParseHistory(cwd, opts.All, filter)
		if err != nil {
			return nil, err
		}
		for i := range parseResult.ToolUses {
			if parseResult.ToolUses[i].Source == "" {
				parseResult.ToolUses[i].Source = "claude"
			}
		}
		allToolUses = append(allToolUses, parseResult.ToolUses...)
	}

	if showCodex {
		codexUses, err := collectCodexHistory(cwd, opts.All)
		if err == nil {
			converted := codexToClaudeToolUses(codexUses)
			converted = claude.FilterToolUses(converted, filter)
			allToolUses = append(allToolUses, converted...)
		}
	}

	sortToolUsesByTime(allToolUses)

	classifier := bash.NewCategoryClassifier(bash.DefaultCategoryConfig())

	var costs []claude.SessionCost
	if opts.Summary {
		if showClaude {
			costs, _ = claude.ParseCostsDetailed(cwd, opts.All, &opts.Since)
		}
		return runHistorySummary(allToolUses, opts, classifier, costs)
	}

	if showClaude {
		costs, _ = claude.ParseCosts(cwd, opts.All, &opts.Since)
	}

	var tl []tools.Tool
	switch {
	case opts.Cost && showClaude && !showCodex:
		tl, err = claude.ParseHistoryTools(cwd, opts.All, filter)
		if err != nil {
			return nil, err
		}
	default:
		tl = claude.ToolUsesToTools(allToolUses)
	}

	if opts.All {
		return runHistoryAll(tl, opts, classifier, costs)
	}
	return runHistorySingle(tl, opts, classifier, costs)
}

// collectCodexHistory loads codex sessions and returns their tool uses
// filtered to the current project (or all if searchAll is true).
func collectCodexHistory(cwd string, searchAll bool) ([]history.ToolUse, error) {
	files, err := history.FindCodexSessionFiles()
	if err != nil {
		return nil, err
	}

	projectInfo := claude.FindProjectInfo(cwd)
	matchRoot := projectInfo.Root
	if matchRoot == "" {
		matchRoot = cwd
	}

	var out []history.ToolUse
	for _, f := range files {
		uses, err := history.ExtractCodexToolUses(f)
		if err != nil || len(uses) == 0 {
			continue
		}
		if !searchAll && !codexSessionMatchesProject(uses, matchRoot) {
			continue
		}
		out = append(out, uses...)
	}
	return out, nil
}

func sortToolUsesByTime(uses []claude.ToolUse) {
	sort.Slice(uses, func(i, j int) bool {
		ti := uses[i].Timestamp
		tj := uses[j].Timestamp
		if ti == nil {
			return false
		}
		if tj == nil {
			return true
		}
		return ti.Before(*tj)
	})
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
			Detail:          buildRowDetail(t, opts),
			Paths:           FormatPathsWithIcons(analysis.ReadPaths, analysis.WritePaths),
			ReadPaths:       analysis.ReadPaths,
			WritePaths:      analysis.WritePaths,
			BinariesDisplay: strings.Join(analysis.Binaries, ", "),
			Binaries:        analysis.Binaries,
			Approved:        approved,
			Time:            base.PrettyTimestamp(),
			Cost:            rowCost(base, opts),
		}
		if opts.Raw {
			row.Raw = base.RawEntry
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
			Detail:          buildRowDetail(t, opts),
			Paths:           FormatPathsWithIcons(analysis.ReadPaths, analysis.WritePaths),
			ReadPaths:       analysis.ReadPaths,
			WritePaths:      analysis.WritePaths,
			BinariesDisplay: strings.Join(analysis.Binaries, ", "),
			Binaries:        analysis.Binaries,
			Approved:        approved,
			Time:            base.PrettyTimestamp(),
			Cost:            rowCost(base, opts),
		}
		if opts.Raw {
			row.Raw = base.RawEntry
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
	categorySet := make(map[string]struct{})

	for _, t := range tl {
		base := t.Base()
		cat := classifyTool(t, classifier)
		categorySet[cat] = struct{}{}

		if len(opts.Categories) > 0 && !collections.MatchItems(cat, opts.Categories...) {
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

	if len(result) == 0 && len(opts.Categories) > 0 {
		categories := make([]string, 0, len(categorySet))
		for c := range categorySet {
			categories = append(categories, c)
		}
		for _, filter := range opts.Categories {
			if similar := captainCollections.FindSimilar(filter, categories, 3); len(similar) > 0 {
				fmt.Fprintf(os.Stderr, "category %q matched nothing. Did you mean: %s?\n", filter, strings.Join(similar, ", "))
			}
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
			if !collections.MatchItems(string(category), opts.Categories...) {
				continue
			}
		}
		filtered = append(filtered, tu)
	}
	return BuildSummary(filtered, classifier, costs), nil
}

func resolvePaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	resolved := make([]string, 0, len(paths))
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = p
		}
		info, err := os.Stat(abs)
		if err == nil && info.IsDir() {
			resolved = append(resolved, abs+"*")
		} else {
			resolved = append(resolved, abs)
		}
	}
	return resolved
}

func reportUnhandledStreamTypes() {
	snap := claude.SnapshotUnhandledStreamTypes()
	if len(snap) == 0 {
		return
	}
	keys := make([]string, 0, len(snap))
	for k := range snap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%d", k, snap[k])
	}
	fmt.Fprintf(os.Stderr, "unhandled stream types: %s\n", strings.Join(parts, ", "))
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
