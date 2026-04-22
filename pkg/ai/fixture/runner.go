// ABOUTME: Runs YAML fixtures by invoking the claude CLI per Run, N times.
// ABOUTME: Aggregates token/cost/duration metrics and compares against a baseline.

package fixture

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Row struct {
	Name        string `json:"name" pretty:"label=Run,width=20,table"`
	Model       string `json:"model" pretty:"label=Model,width=28,table"`
	Status      string `json:"status" pretty:"label=Status,table"`
	Repeat      int    `json:"repeat,omitempty" pretty:"label=N,table"`
	DurationMS  string `json:"durationMs" pretty:"label=Duration,table"`
	DurationStd string `json:"durationStddev,omitempty" pretty:"label=±dur,table"`
	CostUSD     string `json:"costUsd" pretty:"label=Cost,table"`
	Input       int    `json:"inputTokens" pretty:"label=Input,table"`
	Output      int    `json:"outputTokens" pretty:"label=Output,table"`
	CacheRead   int    `json:"cacheReadTokens" pretty:"label=Cache Read,table"`
	CacheWrite  int    `json:"cacheWriteTokens" pretty:"label=Cache Write,table"`
	ToolCalls   int    `json:"toolCalls" pretty:"label=Tools,table"`
	MCPCalls    int    `json:"mcpCalls" pretty:"label=MCP,table"`
	BashCalls   int    `json:"bashCalls" pretty:"label=Bash,table"`
	ToolSummary string `json:"toolSummary,omitempty" pretty:"label=Tool Summary,width=32,table"`
	Speedup     string `json:"speedupVsBaseline,omitempty" pretty:"label=Speedup,table"`
	Cheaper     string `json:"cheaperVsBaseline,omitempty" pretty:"label=Cheaper,table"`
	SessionID   string `json:"sessionId,omitempty" pretty:"label=Session"`
	Error       string `json:"error,omitempty" pretty:"label=Error"`

	DurationMeanMS  float64 `json:"-" pretty:"-"`
	DurationStdevMS float64 `json:"-" pretty:"-"`
	CostMeanUSD     float64 `json:"-" pretty:"-"`
}

type Result struct {
	Name        string `json:"name,omitempty" pretty:"label=Fixture"`
	Description string `json:"description,omitempty" pretty:"label=Description"`
	Baseline    string `json:"baseline,omitempty" pretty:"label=Baseline"`
	Rows        []Row  `json:"rows"`

	Fixture *Fixture `json:"-" pretty:"-"`
}

type Options struct {
	ArtifactDir string
	ClaudeBin   string
}

func Execute(ctx context.Context, f *Fixture, opts Options) (*Result, error) {
	baselineName := f.Baseline
	rows := make([]Row, 0, len(f.Runs))
	rowMetrics := make(map[string]aggregate, len(f.Runs))

	for i, raw := range f.Runs {
		merged := f.Merge(raw)
		if merged.Name == "" {
			merged.Name = fmt.Sprintf("run-%d", i+1)
		}
		if merged.Prompt == "" {
			return nil, fmt.Errorf("run %q is missing prompt", merged.Name)
		}
		if merged.Model == "" {
			return nil, fmt.Errorf("run %q is missing model", merged.Name)
		}
		if baselineName == "" && i == 0 {
			baselineName = merged.Name
		}

		reps := merged.Repeat
		if reps <= 0 {
			reps = f.Repeat
		}
		if reps <= 0 {
			reps = 1
		}

		agg := aggregate{}
		var runErr error
		for iter := 0; iter < reps; iter++ {
			summary, err := executeRun(ctx, f.Dir, merged, opts, iter)
			agg.add(summary)
			if err != nil && runErr == nil {
				runErr = err
			}
			if err != nil {
				break
			}
		}

		agg.finalize()
		rowMetrics[merged.Name] = agg

		row := Row{
			Name:            merged.Name,
			Model:           merged.Model,
			Status:          statusText(agg.Success, runErr, agg.Error),
			Repeat:          agg.N,
			DurationMS:      formatDurationMS(agg.DurationMean),
			CostUSD:         formatUSD(agg.CostMean),
			Input:           agg.Input,
			Output:          agg.Output,
			CacheRead:       agg.CacheRead,
			CacheWrite:      agg.CacheWrite,
			ToolCalls:       agg.ToolCalls,
			MCPCalls:        agg.MCPCalls,
			BashCalls:       agg.BashCalls,
			ToolSummary:     formatToolCounts(agg.ToolCounts),
			SessionID:       agg.SessionID,
			DurationMeanMS:  agg.DurationMean,
			DurationStdevMS: agg.DurationStdev,
			CostMeanUSD:     agg.CostMean,
		}
		if reps > 1 {
			row.DurationStd = formatDurationMS(agg.DurationStdev)
		}
		if runErr != nil {
			row.Error = runErr.Error()
		} else if agg.Error != "" {
			row.Error = agg.Error
		}
		rows = append(rows, row)
	}

	if base, ok := rowMetrics[baselineName]; ok {
		for i := range rows {
			cur := rowMetrics[rows[i].Name]
			rows[i].Speedup = formatRatio(base.DurationMean, cur.DurationMean)
			rows[i].Cheaper = formatRatio(base.CostMean, cur.CostMean)
		}
	}

	return &Result{
		Name:        f.Name,
		Description: f.Description,
		Baseline:    baselineName,
		Rows:        rows,
		Fixture:     f,
	}, nil
}

func executeRun(parent context.Context, fixtureDir string, run Run, opts Options, iter int) (Summary, error) {
	timeout := 5 * time.Minute
	if run.Timeout != "" {
		parsed, err := time.ParseDuration(run.Timeout)
		if err != nil {
			return Summary{}, fmt.Errorf("invalid timeout for %q: %w", run.Name, err)
		}
		timeout = parsed
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	args := []string{"-p", "--output-format", "stream-json", "--model", run.Model, "--max-turns", "1"}
	if run.NoSessionPersistence == nil || *run.NoSessionPersistence {
		args = append(args, "--no-session-persistence")
	}
	if run.System != "" {
		args = append(args, "--system-prompt", run.System)
	}
	if run.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", run.AppendSystemPrompt)
	}
	if run.PermissionMode != "" {
		args = append(args, "--permission-mode", run.PermissionMode)
	}
	if run.Settings != "" {
		args = append(args, "--settings", resolvePath(fixtureDir, run.Settings))
	}
	if run.MaxBudgetUSD != "" {
		args = append(args, "--max-budget-usd", run.MaxBudgetUSD)
	}
	if run.PromptCaching != nil && *run.PromptCaching {
		args = append(args, "--exclude-dynamic-system-prompt-sections")
	}
	if run.StrictMCPConfig != nil && *run.StrictMCPConfig {
		args = append(args, "--strict-mcp-config")
	}
	if run.Bare != nil && *run.Bare {
		args = append(args, "--bare")
	}
	if len(run.Tools) > 0 {
		args = append(args, "--tools")
		args = append(args, run.Tools...)
	}
	if len(run.AllowedTools) > 0 {
		args = append(args, "--allowedTools")
		args = append(args, run.AllowedTools...)
	}
	if len(run.DisallowedTools) > 0 {
		args = append(args, "--disallowedTools")
		args = append(args, run.DisallowedTools...)
	}
	if len(run.Betas) > 0 {
		args = append(args, "--betas")
		args = append(args, run.Betas...)
	}
	if len(run.AddDir) > 0 {
		args = append(args, "--add-dir")
		for _, dir := range run.AddDir {
			args = append(args, resolvePath(fixtureDir, dir))
		}
	}
	if len(run.MCPConfig) > 0 {
		args = append(args, "--mcp-config")
		for _, cfg := range run.MCPConfig {
			args = append(args, resolveMaybeJSONPath(fixtureDir, cfg))
		}
	}
	if len(run.ExtraArgs) > 0 {
		args = append(args, run.ExtraArgs...)
	}
	args = append(args, run.Prompt)

	bin := opts.ClaudeBin
	if bin == "" {
		bin = "claude"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = fixtureDir
	if run.CWD != "" {
		cmd.Dir = resolvePath(fixtureDir, run.CWD)
	}
	cmd.Env = os.Environ()
	for k, v := range run.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	stdout, runErr := cmd.Output()
	if opts.ArtifactDir != "" && len(stdout) > 0 {
		if err := writeArtifact(opts.ArtifactDir, run.Name, iter, stdout); err != nil {
			return Summary{}, fmt.Errorf("writing artifact for %q: %w", run.Name, err)
		}
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			parsed := ParseStream(stdout)
			msg := strings.TrimSpace(string(exitErr.Stderr))
			if msg == "" {
				msg = runErr.Error()
			}
			return parsed, fmt.Errorf("claude run %q failed: %s", run.Name, msg)
		}
		return Summary{}, fmt.Errorf("claude run %q failed: %w", run.Name, runErr)
	}

	return ParseStream(stdout), nil
}

func writeArtifact(baseDir, runName string, iter int, data []byte) error {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return err
	}
	name := fmt.Sprintf("%s-%d.jsonl", runName, iter+1)
	return os.WriteFile(filepath.Join(baseDir, name), data, 0o644)
}

type aggregate struct {
	N             int
	Success       bool
	Error         string
	SessionID     string
	DurationMean  float64
	DurationStdev float64
	CostMean      float64
	Input         int
	Output        int
	CacheRead     int
	CacheWrite    int
	ToolCalls     int
	MCPCalls      int
	BashCalls     int
	ToolCounts    map[string]int

	durations []float64
	costs     []float64
}

func (a *aggregate) add(s Summary) {
	a.N++
	a.durations = append(a.durations, s.DurationMS)
	a.costs = append(a.costs, s.CostUSD)
	if s.SessionID != "" {
		a.SessionID = s.SessionID
	}
	if s.Error != "" && a.Error == "" {
		a.Error = s.Error
	}
	if s.Success {
		a.Success = true
	}
	// Accumulate token/tool counts (sums across repeats — avg derived by dividing in formatter if needed).
	a.Input += s.Input
	a.Output += s.Output
	a.CacheRead += s.CacheRead
	a.CacheWrite += s.CacheWrite
	a.ToolCalls += s.ToolCalls
	a.MCPCalls += s.MCPCalls
	a.BashCalls += s.BashCalls
	if a.ToolCounts == nil {
		a.ToolCounts = map[string]int{}
	}
	for k, v := range s.ToolCounts {
		a.ToolCounts[k] += v
	}
}

func (a *aggregate) finalize() {
	if a.N == 0 {
		return
	}
	a.DurationMean = mean(a.durations)
	a.DurationStdev = stdev(a.durations, a.DurationMean)
	a.CostMean = mean(a.costs)
	// Per-iteration averages for token/tool aggregate counts.
	a.Input /= a.N
	a.Output /= a.N
	a.CacheRead /= a.N
	a.CacheWrite /= a.N
	a.ToolCalls /= a.N
	a.MCPCalls /= a.N
	a.BashCalls /= a.N
	for k, v := range a.ToolCounts {
		a.ToolCounts[k] = v / a.N
	}
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func stdev(xs []float64, m float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	var s float64
	for _, x := range xs {
		d := x - m
		s += d * d
	}
	return math.Sqrt(s / float64(len(xs)-1))
}

func resolvePath(baseDir, value string) string {
	if value == "" {
		return value
	}
	if filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(baseDir, value)
}

func resolveMaybeJSONPath(baseDir, value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return value
	}
	return resolvePath(baseDir, value)
}

func statusText(success bool, err error, resultErr string) string {
	if err != nil || resultErr != "" || !success {
		return "FAIL"
	}
	return "OK"
}

func formatDurationMS(ms float64) string {
	if ms <= 0 {
		return "-"
	}
	return time.Duration(ms * float64(time.Millisecond)).Round(time.Millisecond).String()
}

func formatUSD(v float64) string {
	if v == 0 {
		return "$0"
	}
	return fmt.Sprintf("$%.4f", v)
}

func formatToolCounts(m map[string]int) string {
	if len(m) == 0 {
		return ""
	}
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, fmt.Sprintf("%s:%d", k, v))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

func formatRatio(base, current float64) string {
	if base <= 0 || current <= 0 {
		return ""
	}
	return fmt.Sprintf("%.2fx", base/current)
}
