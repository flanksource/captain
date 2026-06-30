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
	Paths      []string `args:"true" help:"Filter by file or directory paths; canonical UUID args are treated as session IDs"`
	File       string   `flag:"file" help:"Read from a JSONL/JSON file ('-' for stdin) instead of session history" short:"f"`
	Tools      []string `flag:"tool" help:"Filter by tool patterns" short:"t"`
	Categories []string `flag:"category" help:"Filter by category patterns" short:"c"`
	Approved   string   `flag:"approved" help:"Filter by approval status (true=approved, false=denied)"`
	Session    string   `flag:"session" help:"Alias for --session-id (exact or prefix match)"`
	SessionID  string   `flag:"session-id" help:"Filter by session ID (exact or prefix match)"`
	TextFilter string
	Limit      int       `flag:"limit" help:"Maximum results" default:"100" short:"l"`
	Since      time.Time `flag:"since" help:"Only include commands after this time" default:"now-7d" short:"s"`
	All        bool      `flag:"all" help:"Search all projects, not just current directory" short:"a"`
	Claude     bool      `flag:"claude" help:"Show only Claude history"`
	Codex      bool      `flag:"codex" help:"Show only Codex history"`
	Last       bool      `flag:"last" help:"Show only the most-recent session"`
	Short      bool      `flag:"short" help:"Compact output without diffs and code blocks" short:"S"`
	Compact    bool      `flag:"compact" help:"Single line per entry" short:"C"`
	Summary    bool      `flag:"summary" help:"Show aggregate summary instead of individual tool uses"`
	Cost       bool      `flag:"cost" help:"Include per-row token breakdown and dollar cost in row detail"`
	Raw        bool      `flag:"raw" help:"Include the raw Claude session JSONL line in row detail"`
	Debug      bool      `flag:"debug" help:"Include original Claude history struct in results"`
	Agents     bool      `flag:"agents" help:"Include tool calls from nested sub-agents (Task/Agent); --agents=false for the main thread only" default:"true"`
	Plans      bool      `flag:"plans" help:"Include writes to plan files (~/.claude/plans); --plans=true to show them"`
	Ignored    bool      `flag:"ignored" help:"Include writes to gitignored / out-of-repo files; --ignored=true to show them"`
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

	var sessionIDs []string
	var err error
	opts, sessionIDs, err = normalizeHistoryOptions(opts)
	if err != nil {
		return nil, err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	cwd = historyScanCWD(cwd, opts.Paths, opts.All)

	filter := claude.Filter{
		Tools:         opts.Tools,
		Paths:         resolvePaths(opts.Paths),
		Since:         &opts.Since,
		SessionID:     firstSessionID(sessionIDs),
		SessionIDs:    sessionIDs,
		IncludeAgents: opts.Agents,
	}

	if len(opts.Categories) == 0 && opts.TextFilter == "" && !opts.Last {
		filter.Limit = opts.Limit
	}

	showClaude := opts.Claude || (!opts.Claude && !opts.Codex)
	showCodex := opts.Codex || (!opts.Claude && !opts.Codex)

	allToolUses, err := gatherToolUses(cwd, opts.All, showClaude, showCodex, filter)
	if err != nil {
		return nil, err
	}

	sortToolUsesByTime(allToolUses)

	classifier := bash.NewCategoryClassifier(bash.DefaultCategoryConfig())

	var costs []claude.SessionCost
	if opts.Summary {
		if showClaude {
			costs, _ = claude.ParseCostsDetailedWithFilter(cwd, opts.All, &opts.Since, filter)
			costs = filterCostsBySessionID(costs, sessionIDs)
		}
		return runHistorySummary(allToolUses, opts, classifier, costs)
	}

	if showClaude {
		costs, _ = claude.ParseCostsWithFilter(cwd, opts.All, &opts.Since, filter)
		costs = filterCostsBySessionID(costs, sessionIDs)
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

	if !opts.Plans || !opts.Ignored {
		tl = filterToolsByPath(tl, newPathFilter(opts.Plans, opts.Ignored))
	}

	if opts.Last {
		tl = lastSessionTools(tl)
		// --last means "the whole most-recent session" — don't let the row
		// limit clip the session in the downstream filter pass.
		opts.Limit = 0
	}

	if opts.All {
		return runHistoryAll(tl, opts, classifier, costs)
	}
	return runHistorySingle(tl, opts, classifier, costs)
}

func normalizeHistoryOptions(opts HistoryOptions) (HistoryOptions, []string, error) {
	positionalIDs, paths := splitSessionIDPathArgs(opts.Paths)
	sessionIDs, err := normalizeSessionIDFilters(opts.Session, opts.SessionID, positionalIDs)
	if err != nil {
		return opts, nil, err
	}
	opts.Paths = paths
	return opts, sessionIDs, nil
}

// lastSessionTools returns the trailing run of tools that share a sessionKey
// with the final tool. The input is expected to be sorted oldest-first; the
// result preserves that order.
func lastSessionTools(tl []tools.Tool) []tools.Tool {
	if len(tl) == 0 {
		return tl
	}
	last := keyForTool(tl[len(tl)-1])
	start := len(tl) - 1
	for start > 0 && keyForTool(tl[start-1]) == last {
		start--
	}
	return tl[start:]
}

// gatherToolUses collects Claude and Codex tool uses for the working directory,
// tags each with its source, and applies the filter (including any session ID)
// to both. It is the shared front-end for the history and changes commands.
func gatherToolUses(cwd string, searchAll, showClaude, showCodex bool, filter claude.Filter) ([]claude.ToolUse, error) {
	var allToolUses []claude.ToolUse

	if showClaude {
		parseResult, err := claude.ParseHistory(cwd, searchAll, filter)
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
		codexUses, err := collectCodexHistory(cwd, searchAll, filter)
		if err == nil {
			converted := codexToClaudeToolUses(codexUses)
			converted = claude.FilterToolUses(converted, filter)
			allToolUses = append(allToolUses, converted...)
		}
	}

	return allToolUses, nil
}

// collectCodexHistory loads codex sessions and returns their tool uses
// filtered to the current project (or all if searchAll is true).
func collectCodexHistory(cwd string, searchAll bool, filter claude.Filter) ([]history.ToolUse, error) {
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
		info, infoErr := history.ReadCodexSessionMeta(f)
		if infoErr == nil && info != nil {
			if info.ID != "" && !filter.MatchesSessionID(info.ID) {
				continue
			}
			if !searchAll && info.CWD != "" && !codexCWDMatchesProject(info.CWD, matchRoot) {
				continue
			}
			// When a session-id filter is set but the metadata has no id yet, fall
			// through to full parsing: older/live schemas may only expose the session
			// id on later events.
		}

		uses, err := history.ExtractCodexToolUses(f)
		if err != nil || len(uses) == 0 {
			continue
		}
		if !codexUsesMatchSession(uses, filter) {
			continue
		}
		if !searchAll && !codexSessionMatchesProject(uses, matchRoot) {
			continue
		}
		out = append(out, uses...)
	}
	return out, nil
}

func codexUsesMatchSession(uses []history.ToolUse, filter claude.Filter) bool {
	if !filter.HasSessionIDFilter() {
		return true
	}
	for _, use := range uses {
		if filter.MatchesSessionID(use.SessionID) {
			return true
		}
	}
	return false
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
			Agent:           toolAgentLabel(base),
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
			Agent:           toolAgentLabel(base),
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

		if len(opts.Categories) > 0 && !matchCategoryFilters(categoryFilterCandidates(t, cat), opts.Categories) {
			continue
		}
		if !matchApprovedFilter(opts.Approved, base.Denied) {
			continue
		}
		if !matchesHistoryTextFilter(t, cat, opts.TextFilter) {
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

func matchCategoryFilters(candidates []string, filters []string) bool {
	filters = normalizeCategoryFilters(filters)
	if len(filters) == 0 {
		return true
	}

	hasInclude := false
	for _, filter := range filters {
		if strings.HasPrefix(filter, "!") {
			pattern := strings.TrimSpace(strings.TrimPrefix(filter, "!"))
			if pattern != "" && matchesAnyCategoryCandidate(candidates, pattern) {
				return false
			}
			continue
		}

		hasInclude = true
		if matchesAnyCategoryCandidate(candidates, filter) {
			return true
		}
	}
	return !hasInclude
}

func normalizeCategoryFilters(filters []string) []string {
	var out []string
	for _, filter := range filters {
		for _, part := range strings.Split(filter, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func matchesAnyCategoryCandidate(candidates []string, pattern string) bool {
	for _, candidate := range candidates {
		if candidate != "" && collections.MatchItems(candidate, pattern) {
			return true
		}
	}
	return false
}

func categoryFilterCandidates(t tools.Tool, category string) []string {
	base := t.Base()
	return uniqueNonEmpty(
		category,
		t.Name(),
		base.RawTool,
		messageAlias(t.Name()),
	)
}

func toolUseCategoryFilterCandidates(tu claude.ToolUse, category string) []string {
	return uniqueNonEmpty(
		category,
		tu.Tool,
		tu.DisplayTool(),
		messageAlias(tu.DisplayTool()),
	)
}

func messageAlias(tool string) string {
	switch strings.ToLower(tool) {
	case "assistant", "reasoning":
		return "message"
	default:
		return ""
	}
}

func uniqueNonEmpty(values ...string) []string {
	seen := make(map[string]struct{}, len(values))
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

func HistoryTextFilterFromGlobal(filter string) string {
	filter = strings.TrimSpace(filter)
	if filter == "" || strings.ContainsAny(filter, "=<>!&|()'\"") {
		return ""
	}
	return filter
}

func matchesHistoryTextFilter(t tools.Tool, category, filter string) bool {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" {
		return true
	}

	base := t.Base()
	values := []string{
		t.Name(),
		category,
		t.FilePath(),
		t.ExtractPath(),
		base.RawTool,
		base.CWD,
		base.ProjectRoot,
		base.SessionID,
		base.ToolUseID,
		base.DeniedReason,
	}
	for k, v := range base.Input {
		values = append(values, k, fmt.Sprint(v))
	}

	for _, value := range values {
		if strings.Contains(strings.ToLower(value), filter) {
			return true
		}
	}
	return false
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
		category := classifyToolUse(tu, classifier)
		if len(opts.Categories) > 0 {
			if !matchCategoryFilters(toolUseCategoryFilterCandidates(tu, category), opts.Categories) {
				continue
			}
		}
		if !matchesToolUseTextFilter(tu, category, opts.TextFilter) {
			continue
		}
		filtered = append(filtered, tu)
	}
	return BuildSummary(filtered, classifier, costs), nil
}

func classifyToolUse(tu claude.ToolUse, classifier *bash.CategoryClassifier) string {
	category := classifier.ClassifyToolWithPath(tu.Tool, tu.FilePath())
	if category == bash.CategoryOther && tu.Tool == "Bash" {
		if rawCmd, ok := tu.Input["command"].(string); ok {
			category = classifier.ClassifyBash(rawCmd)
		}
	}
	return string(category)
}

func matchesToolUseTextFilter(tu claude.ToolUse, category, filter string) bool {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" {
		return true
	}

	values := []string{
		tu.Tool,
		tu.DisplayTool(),
		category,
		tu.FilePath(),
		tu.FormatCommand(),
		tu.CWD,
		tu.ProjectRoot,
		tu.SessionID,
		tu.ToolUseID,
		tu.DeniedReason,
	}
	for k, v := range tu.Input {
		values = append(values, k, fmt.Sprint(v))
	}

	for _, value := range values {
		if strings.Contains(strings.ToLower(value), filter) {
			return true
		}
	}
	return false
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
