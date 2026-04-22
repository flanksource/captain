// ABOUTME: Writes a markdown evidence report for a fixture Result.
// ABOUTME: Includes fixture metadata, per-run config, metrics table, and baseline callout.

package fixture

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

func WriteMarkdown(w io.Writer, r *Result) error {
	bw := &mdBuf{w: w}

	title := r.Name
	if title == "" {
		title = "AI Fixture Result"
	}
	bw.writef("# %s\n\n", title)
	if r.Description != "" {
		bw.writef("%s\n\n", r.Description)
	}
	bw.writef("_Generated %s_\n\n", time.Now().UTC().Format(time.RFC3339))

	bw.writeBaselineCallout(r)
	bw.writeMetricsTable(r)
	bw.writeConfigSection(r)

	return bw.err
}

type mdBuf struct {
	w   io.Writer
	err error
}

func (b *mdBuf) writef(format string, args ...any) {
	if b.err != nil {
		return
	}
	_, b.err = fmt.Fprintf(b.w, format, args...)
}

func (b *mdBuf) writeBaselineCallout(r *Result) {
	if r.Baseline == "" || len(r.Rows) < 2 {
		return
	}
	var base *Row
	others := make([]*Row, 0, len(r.Rows)-1)
	for i := range r.Rows {
		row := &r.Rows[i]
		if row.Name == r.Baseline {
			base = row
		} else {
			others = append(others, row)
		}
	}
	if base == nil {
		return
	}
	b.writef("## Headline\n\n")
	b.writef("Baseline: `%s` — %s, %s\n\n", base.Name, base.DurationMS, base.CostUSD)
	for _, o := range others {
		speed := compareRatio(base.DurationMeanMS, o.DurationMeanMS, "faster", "slower")
		cheap := compareRatio(base.CostMeanUSD, o.CostMeanUSD, "cheaper", "pricier")
		b.writef("- **`%s`**: %s, %s vs baseline\n", o.Name, speed, cheap)
	}
	b.writef("\n")
}

func compareRatio(base, other float64, betterLabel, worseLabel string) string {
	if base <= 0 || other <= 0 {
		return "n/a"
	}
	if other <= base {
		return fmt.Sprintf("%.2fx %s", base/other, betterLabel)
	}
	return fmt.Sprintf("%.2fx %s", other/base, worseLabel)
}

func (b *mdBuf) writeMetricsTable(r *Result) {
	b.writef("## Metrics\n\n")
	b.writef("| Run | Model | N | Duration | ±Stdev | Cost | Input | Output | Cache R/W | Tools | MCP | Bash | Speedup | Cheaper | Status |\n")
	b.writef("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, row := range r.Rows {
		stdev := row.DurationStd
		if stdev == "" {
			stdev = "-"
		}
		cache := fmt.Sprintf("%d / %d", row.CacheRead, row.CacheWrite)
		b.writef("| %s | %s | %d | %s | %s | %s | %d | %d | %s | %d | %d | %d | %s | %s | %s |\n",
			row.Name, row.Model, row.Repeat,
			row.DurationMS, stdev, row.CostUSD,
			row.Input, row.Output, cache,
			row.ToolCalls, row.MCPCalls, row.BashCalls,
			dash(row.Speedup), dash(row.Cheaper), row.Status,
		)
	}
	b.writef("\n")
}

func (b *mdBuf) writeConfigSection(r *Result) {
	if r.Fixture == nil {
		return
	}
	b.writef("## Run configurations\n\n")
	for _, raw := range r.Fixture.Runs {
		merged := r.Fixture.Merge(raw)
		b.writef("### `%s`\n\n", merged.Name)
		b.writef("- Model: `%s`\n", merged.Model)
		if merged.PromptCaching != nil {
			b.writef("- Prompt caching: `%v`\n", *merged.PromptCaching)
		}
		if len(merged.Tools) > 0 {
			b.writef("- Tools: `%s`\n", strings.Join(merged.Tools, ", "))
		}
		if len(merged.AllowedTools) > 0 {
			b.writef("- Allowed: `%s`\n", strings.Join(merged.AllowedTools, ", "))
		}
		if len(merged.MCPConfig) > 0 {
			b.writef("- MCP config: `%s`\n", strings.Join(merged.MCPConfig, ", "))
		}
		if merged.StrictMCPConfig != nil && *merged.StrictMCPConfig {
			b.writef("- Strict MCP: `true`\n")
		}
		b.writef("\n")
	}

	if prompt := fixturePrompt(r.Fixture); prompt != "" {
		b.writef("## Prompt\n\n```\n%s\n```\n\n", strings.TrimSpace(prompt))
	}

	if len(r.Rows) > 0 {
		b.writef("## Tool usage\n\n")
		for _, row := range r.Rows {
			if row.ToolSummary == "" {
				continue
			}
			b.writef("- `%s`: %s\n", row.Name, formatToolUsage(row.ToolSummary))
		}
		b.writef("\n")
	}
}

func fixturePrompt(f *Fixture) string {
	if f.Prompt != "" {
		return f.Prompt
	}
	for _, r := range f.Runs {
		if r.Prompt != "" {
			return r.Prompt
		}
	}
	return ""
}

func formatToolUsage(summary string) string {
	parts := strings.Split(summary, ", ")
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
