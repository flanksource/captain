// ABOUTME: Renders a fixture Result as a markdown / HTML / ANSI evidence report.
// ABOUTME: Uses clicky TextTable for the metrics grid so all formats share one source of truth.

package fixture

import (
	"fmt"
	"html"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/clicky/api"
)

type Format string

const (
	FormatMarkdown Format = "markdown"
	FormatHTML     Format = "html"
	FormatANSI     Format = "ansi"
)

func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "markdown", "md":
		return FormatMarkdown, nil
	case "html":
		return FormatHTML, nil
	case "ansi", "term", "terminal":
		return FormatANSI, nil
	}
	return "", fmt.Errorf("unsupported format %q (want markdown, html, ansi)", s)
}

func WriteReport(w io.Writer, r *Result, format Format) error {
	b := newReportBuf(w, format)

	title := r.Name
	if title == "" {
		title = "AI Fixture Result"
	}
	b.heading(1, title)
	if r.Description != "" {
		b.paragraph(r.Description)
	}
	b.paragraph(fmt.Sprintf("_Generated %s_", time.Now().UTC().Format(time.RFC3339)))

	b.writeBaselineCallout(r)
	b.writeMetricsTable(r)
	b.writeFindings(r)
	b.writeConfigSection(r)

	return b.err
}

type reportBuf struct {
	w      io.Writer
	format Format
	err    error
}

func newReportBuf(w io.Writer, f Format) *reportBuf {
	return &reportBuf{w: w, format: f}
}

func (b *reportBuf) writef(format string, args ...any) {
	if b.err != nil {
		return
	}
	_, b.err = fmt.Fprintf(b.w, format, args...)
}

func (b *reportBuf) raw(s string) {
	if b.err != nil {
		return
	}
	_, b.err = io.WriteString(b.w, s)
}

func (b *reportBuf) heading(level int, text string) {
	switch b.format {
	case FormatHTML:
		b.writef("<h%d>%s</h%d>\n\n", level, html.EscapeString(text), level)
	case FormatANSI:
		b.writef("\n%s\n%s\n\n", text, strings.Repeat("─", len(text)))
	default:
		b.writef("%s %s\n\n", strings.Repeat("#", level), text)
	}
}

func (b *reportBuf) paragraph(text string) {
	switch b.format {
	case FormatHTML:
		b.writef("<p>%s</p>\n\n", html.EscapeString(text))
	default:
		b.writef("%s\n\n", text)
	}
}

func (b *reportBuf) bullet(text string) {
	switch b.format {
	case FormatHTML:
		b.writef("<li>%s</li>\n", text)
	default:
		b.writef("- %s\n", text)
	}
}

func (b *reportBuf) listOpen() {
	if b.format == FormatHTML {
		b.raw("<ul>\n")
	}
}

func (b *reportBuf) listClose() {
	switch b.format {
	case FormatHTML:
		b.raw("</ul>\n\n")
	default:
		b.raw("\n")
	}
}

func (b *reportBuf) codeBlock(content string) {
	content = strings.TrimSpace(content)
	switch b.format {
	case FormatHTML:
		b.writef("<pre><code>%s</code></pre>\n\n", html.EscapeString(content))
	case FormatANSI:
		b.writef("%s\n\n", indentLines(content, "    "))
	default:
		b.writef("```\n%s\n```\n\n", content)
	}
}

func (b *reportBuf) renderTable(t api.TextTable) {
	switch b.format {
	case FormatHTML:
		b.writef("%s\n", t.StaticHTML())
	case FormatANSI:
		b.writef("%s\n", t.ANSI())
	default:
		b.writef("%s\n", t.Markdown())
	}
}

func (b *reportBuf) writeBaselineCallout(r *Result) {
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
	b.heading(2, "Headline")
	b.paragraph(fmt.Sprintf("Baseline: `%s` — %s, %s", base.Name, base.DurationMS, base.CostUSD))
	b.listOpen()
	for _, o := range others {
		speed := compareRatio(base.DurationMeanMS, o.DurationMeanMS, "faster", "slower")
		cheap := compareRatio(base.CostMeanUSD, o.CostMeanUSD, "cheaper", "pricier")
		b.bullet(fmt.Sprintf("**`%s`**: %s, %s vs baseline", o.Name, speed, cheap))
	}
	b.listClose()
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

var metricsHeaders = []string{
	"Run", "Model", "N", "Duration", "±Stdev", "Cost",
	"Input", "Output", "Cache R/W", "Tools", "MCP", "Bash",
	"Speedup", "Cheaper", "Status",
}

func (b *reportBuf) writeMetricsTable(r *Result) {
	b.heading(2, "Metrics")

	table := api.TextTable{
		Headers:    headerList(metricsHeaders),
		FieldNames: metricsHeaders,
	}
	for _, row := range r.Rows {
		stdev := row.DurationStd
		if stdev == "" {
			stdev = "-"
		}
		cells := map[string]string{
			"Run":       row.Name,
			"Model":     row.Model,
			"N":         fmt.Sprintf("%d", row.Repeat),
			"Duration":  row.DurationMS,
			"±Stdev":    stdev,
			"Cost":      row.CostUSD,
			"Input":     fmt.Sprintf("%d", row.Input),
			"Output":    fmt.Sprintf("%d", row.Output),
			"Cache R/W": fmt.Sprintf("%d / %d", row.CacheRead, row.CacheWrite),
			"Tools":     fmt.Sprintf("%d", row.ToolCalls),
			"MCP":       fmt.Sprintf("%d", row.MCPCalls),
			"Bash":      fmt.Sprintf("%d", row.BashCalls),
			"Speedup":   dash(row.Speedup),
			"Cheaper":   dash(row.Cheaper),
			"Status":    row.Status,
		}
		table.Rows = append(table.Rows, toTableRow(cells))
	}

	b.renderTable(table)
}

func (b *reportBuf) writeFindings(r *Result) {
	any := false
	for _, row := range r.Rows {
		if strings.TrimSpace(row.Result) != "" {
			any = true
			break
		}
	}
	if !any {
		return
	}
	b.heading(2, "Findings")
	for _, row := range r.Rows {
		text := strings.TrimSpace(row.Result)
		if text == "" {
			continue
		}
		b.heading(3, fmt.Sprintf("`%s`", row.Name))
		b.paragraph(text)
	}
}

func (b *reportBuf) writeConfigSection(r *Result) {
	if r.Fixture == nil {
		return
	}
	b.heading(2, "Run configurations")
	for _, raw := range r.Fixture.Runs {
		merged := r.Fixture.Merge(raw)
		b.heading(3, fmt.Sprintf("`%s`", merged.Name))
		b.listOpen()
		b.bullet(fmt.Sprintf("Model: `%s`", merged.Model))
		if merged.PromptCaching != nil {
			b.bullet(fmt.Sprintf("Prompt caching: `%v`", *merged.PromptCaching))
		}
		if len(merged.Tools) > 0 {
			b.bullet(fmt.Sprintf("Tools: `%s`", strings.Join(merged.Tools, ", ")))
		}
		if len(merged.AllowedTools) > 0 {
			b.bullet(fmt.Sprintf("Allowed: `%s`", strings.Join(merged.AllowedTools, ", ")))
		}
		if len(merged.MCPConfig) > 0 {
			b.bullet(fmt.Sprintf("MCP config: `%s`", strings.Join(merged.MCPConfig, ", ")))
		} else {
			b.bullet("MCP: none")
		}
		b.listClose()
	}

	if prompt := fixturePrompt(r.Fixture); prompt != "" {
		b.heading(2, "Prompt")
		b.codeBlock(prompt)
	}

	if len(r.Rows) > 0 {
		b.heading(2, "Tool usage")
		b.listOpen()
		for _, row := range r.Rows {
			if row.ToolSummary == "" {
				continue
			}
			b.bullet(fmt.Sprintf("`%s`: %s", row.Name, formatToolUsage(row.ToolSummary)))
		}
		b.listClose()
	}
}

func headerList(names []string) api.TextList {
	out := make(api.TextList, 0, len(names))
	for _, n := range names {
		out = append(out, api.Text{Content: n})
	}
	return out
}

func toTableRow(cells map[string]string) api.TableRow {
	row := api.TableRow{}
	for k, v := range cells {
		row[k] = api.TypedValue{Textable: api.Text{Content: v}}
	}
	return row
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

func indentLines(s, indent string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = indent + l
	}
	return strings.Join(lines, "\n")
}
