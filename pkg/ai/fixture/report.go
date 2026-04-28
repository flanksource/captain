// ABOUTME: Renders a fixture Result as a markdown / HTML / ANSI evidence report.
// ABOUTME: Uses clicky TextTable for the metrics grid so all formats share one source of truth.

package fixture

import (
	"bytes"
	"fmt"
	"html"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/flanksource/clicky/api"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// MarkdownDoc is a free-form markdown document that satisfies clicky's
// Textable interface — String/ANSI keep the raw markdown (terminals render it
// readably as-is), Markdown returns the source, HTML runs it through goldmark
// with GFM tables enabled. Lives here for now; promote to clicky if it ever
// gets reused.
type MarkdownDoc struct {
	Source string
}

func (m MarkdownDoc) String() string   { return m.Source }
func (m MarkdownDoc) ANSI() string     { return m.Source }
func (m MarkdownDoc) Markdown() string { return m.Source }
func (m MarkdownDoc) HTML() string {
	md := goldmark.New(goldmark.WithExtensions(extension.GFM))
	var buf bytes.Buffer
	if err := md.Convert([]byte(m.Source), &buf); err != nil {
		return html.EscapeString(m.Source)
	}
	return buf.String()
}

// compile-time check that MarkdownDoc satisfies clicky's Textable.
var _ api.Textable = MarkdownDoc{}

var (
	mdInlineBold = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	mdInlineCode = regexp.MustCompile("`([^`\n]+)`")
	mdInlineEm   = regexp.MustCompile(`(^|[\s(])_([^_\n]+)_($|[\s.,;:)])`)
)

// inlineMarkdownToHTML escapes HTML special chars first, then converts the
// limited inline markdown we emit (`code`, **bold**, _em_) into HTML tags.
func inlineMarkdownToHTML(s string) string {
	s = html.EscapeString(s)
	s = mdInlineBold.ReplaceAllString(s, "<strong>$1</strong>")
	s = mdInlineCode.ReplaceAllString(s, "<code>$1</code>")
	s = mdInlineEm.ReplaceAllString(s, "$1<em>$2</em>$3")
	return s
}

// stripInlineMarkdown removes the markdown markers we use, leaving readable
// plain text for ANSI mode.
func stripInlineMarkdown(s string) string {
	s = mdInlineBold.ReplaceAllString(s, "$1")
	s = mdInlineCode.ReplaceAllString(s, "$1")
	s = mdInlineEm.ReplaceAllString(s, "$1$2$3")
	return s
}


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

	if format == FormatHTML {
		b.writeHTMLHeader(title)
	}

	b.heading(1, title)
	if r.Description != "" {
		b.paragraph(r.Description)
	}
	b.paragraph(fmt.Sprintf("_Generated %s_", time.Now().UTC().Format(time.RFC3339)))

	b.writeBaselineCallout(r)
	b.writeMetricsTable(r)
	b.writeConfigSection(r)
	b.writePerRunSections(r)

	if format == FormatHTML {
		b.writeHTMLFooter()
	}

	return b.err
}

// reportCSS extends Tailwind defaults so our markdown-rendered findings (which
// goldmark emits as plain <h1>/<p>/<ul>/<table>/<pre>) look readable. Tailwind
// resets most of those, so without these rules headings collapse and lists
// lose their bullets.
const reportCSS = `
.finding h1 { font-size: 1.5rem; font-weight: 700; margin: 1.25rem 0 0.5rem; }
.finding h2 { font-size: 1.25rem; font-weight: 600; margin: 1rem 0 0.5rem; color: #1f2937; }
.finding h3 { font-size: 1.05rem; font-weight: 600; margin: 0.75rem 0 0.4rem; color: #374151; }
.finding p  { margin: 0.5rem 0; line-height: 1.6; }
.finding ul { list-style: disc; margin: 0.5rem 0 0.5rem 1.5rem; }
.finding ol { list-style: decimal; margin: 0.5rem 0 0.5rem 1.5rem; }
.finding li { margin: 0.2rem 0; }
.finding code { background: #f3f4f6; padding: 0.1rem 0.3rem; border-radius: 3px; font-size: 0.9em; font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace; }
.finding pre { margin: 0.75rem 0; border-radius: 6px; overflow: hidden; background: #282c34; }
.finding pre code, .finding pre code.hljs { display: block; background: #282c34 !important; color: #abb2bf !important; padding: 0.85rem 1rem; border-radius: 6px; overflow-x: auto; font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace; font-size: 0.9em; }
.finding pre code .hljs-keyword, .finding pre code .hljs-selector-tag, .finding pre code .hljs-built_in, .finding pre code .hljs-name, .finding pre code .hljs-tag { color: #c678dd !important; }
.finding pre code .hljs-string, .finding pre code .hljs-attr, .finding pre code .hljs-symbol, .finding pre code .hljs-bullet, .finding pre code .hljs-addition { color: #98c379 !important; }
.finding pre code .hljs-number, .finding pre code .hljs-literal, .finding pre code .hljs-meta, .finding pre code .hljs-link { color: #d19a66 !important; }
.finding pre code .hljs-comment, .finding pre code .hljs-quote { color: #5c6370 !important; font-style: italic; }
.finding pre code .hljs-title, .finding pre code .hljs-section, .finding pre code .hljs-function .hljs-title { color: #61afef !important; }
.finding pre code .hljs-variable, .finding pre code .hljs-template-variable, .finding pre code .hljs-attribute, .finding pre code .hljs-deletion { color: #e06c75 !important; }
.finding pre code .hljs-type, .finding pre code .hljs-class .hljs-title { color: #e6c07b !important; }
.finding table { border-collapse: collapse; width: 100%; margin: 0.75rem 0; font-size: 0.95em; }
.finding th, .finding td { border: 1px solid #d1d5db; padding: 0.4rem 0.6rem; text-align: left; }
.finding th { background: #f3f4f6; font-weight: 600; }

.finding-header { display: flex; align-items: baseline; gap: 0.75rem; margin: 1.75rem 0 0.5rem; padding: 0.6rem 1rem; background: #1e3a8a; color: #fff; border-radius: 6px; box-shadow: 0 1px 3px rgba(0,0,0,0.1); }
.finding-header .finding-name { font-size: 1.4rem; font-weight: 700; font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace; }
.finding-header .finding-model { font-size: 0.85rem; opacity: 0.75; font-weight: 500; }

h1.report-title { font-size: 2rem; font-weight: 700; margin: 0 0 0.5rem; }
h2.report-section { font-size: 1.5rem; font-weight: 600; margin: 1.5rem 0 0.75rem; padding-bottom: 0.25rem; border-bottom: 1px solid #e5e7eb; }
h3.report-subsection { font-size: 1.15rem; font-weight: 600; margin: 1rem 0 0.5rem; color: #374151; }
.report ul { list-style: disc; margin-left: 1.5rem; margin-bottom: 0.75rem; }
.report li { margin: 0.2rem 0; }
.report pre { margin: 0.75rem 0; border-radius: 6px; overflow: hidden; background: #282c34; }
.report pre code, .report pre code.hljs { display: block; background: #282c34 !important; color: #abb2bf !important; padding: 0.85rem 1rem; overflow-x: auto; font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace; font-size: 0.9em; }

.tool-calls { display: flex; flex-direction: column; border: 1px solid #d1d5db; border-radius: 6px; overflow: hidden; margin: 0.75rem 0; }
.tool-calls .tc-row { display: grid; grid-template-columns: 110px minmax(0, 1fr) 110px 90px 60px; gap: 0.5rem; padding: 0.5rem 0.75rem; align-items: center; border-bottom: 1px solid #e5e7eb; background: #fff; }
.tool-calls .tc-row:last-child { border-bottom: 0; }
.tool-calls .tc-header { background: #f3f4f6; font-weight: 600; font-size: 0.85em; color: #4b5563; text-transform: uppercase; letter-spacing: 0.04em; }
.tool-calls details.tc-row { display: block; padding: 0; }
.tool-calls details.tc-row .tc-summary { display: grid; grid-template-columns: 110px minmax(0, 1fr) 110px 90px 60px; gap: 0.5rem; padding: 0.5rem 0.75rem; align-items: center; cursor: pointer; list-style: none; }
.tool-calls details.tc-row .tc-summary::-webkit-details-marker { display: none; }
.tool-calls details.tc-row .tc-summary:hover { background: #f9fafb; }
.tool-calls details.tc-row[open] .tc-summary { background: #eef2ff; border-bottom: 1px solid #e5e7eb; }
.tool-calls .tc-time { font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace; font-size: 0.85em; color: #6b7280; }
.tool-calls .tc-cmd { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tool-calls .tc-cmd code { background: transparent; padding: 0; font-size: 0.9em; color: #111827; }
.tool-calls .tc-tokens, .tool-calls .tc-dur, .tool-calls .tc-net { font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace; font-size: 0.85em; color: #4b5563; }
.tool-calls .tc-output { margin: 0; padding: 0.85rem 1rem; background: #282c34 !important; color: #abb2bf !important; border-radius: 0; max-height: 480px; overflow: auto; white-space: pre-wrap; word-break: break-word; }
.report p  { margin: 0.5rem 0; }
`

func (b *reportBuf) writeHTMLHeader(title string) {
	b.writef(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>%s</title>
<script src="https://cdn.tailwindcss.com"></script>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11.10.0/build/styles/atom-one-dark.min.css">
<script src="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11.10.0/build/highlight.min.js"></script>
<style>%s</style>
</head>
<body class="bg-gray-50 min-h-screen text-gray-800">
<div class="max-w-6xl mx-auto px-6 py-8 report">
`, html.EscapeString(title), reportCSS)
}

func (b *reportBuf) writeHTMLFooter() {
	b.raw(`<script>document.querySelectorAll('pre code').forEach(el => { if (window.hljs) hljs.highlightElement(el); });</script>`)
	b.raw("\n</div>\n</body>\n</html>\n")
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
		var class string
		switch level {
		case 1:
			class = ` class="report-title"`
		case 2:
			class = ` class="report-section"`
		case 3:
			class = ` class="report-subsection"`
		}
		b.writef("<h%d%s>%s</h%d>\n\n", level, class, inlineMarkdownToHTML(text), level)
	case FormatANSI:
		plain := stripInlineMarkdown(text)
		b.writef("\n%s\n%s\n\n", plain, strings.Repeat("─", len(plain)))
	default:
		b.writef("%s %s\n\n", strings.Repeat("#", level), text)
	}
}

func (b *reportBuf) paragraph(text string) {
	switch b.format {
	case FormatHTML:
		b.writef("<p>%s</p>\n\n", inlineMarkdownToHTML(text))
	case FormatANSI:
		b.writef("%s\n\n", stripInlineMarkdown(text))
	default:
		b.writef("%s\n\n", text)
	}
}

func (b *reportBuf) bullet(text string) {
	switch b.format {
	case FormatHTML:
		b.writef("<li>%s</li>\n", inlineMarkdownToHTML(text))
	case FormatANSI:
		b.writef("- %s\n", stripInlineMarkdown(text))
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

// richBlock renders a free-form markdown document (e.g. claude's findings)
// via the Textable interface so each format gets its proper representation.
func (b *reportBuf) richBlock(content string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}
	doc := MarkdownDoc{Source: content}
	switch b.format {
	case FormatHTML:
		b.writef("<div class=\"finding\">\n%s</div>\n\n", doc.HTML())
	case FormatANSI:
		b.writef("%s\n\n", doc.ANSI())
	default:
		b.writef("%s\n\n", doc.Markdown())
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

// writePerRunSections groups everything that pertains to a single run —
// findings markdown, tool calls (each row collapsible to show output), and
// the proxy network logs — under one prominent header per run.
func (b *reportBuf) writePerRunSections(r *Result) {
	if !anyRunHasContent(r) {
		return
	}
	if r.KubectlProxy != "" {
		b.paragraph(fmt.Sprintf("Kubernetes proxy endpoint: `%s`", r.KubectlProxy))
	}
	for _, info := range r.MCPProxies {
		b.paragraph(fmt.Sprintf("MCP proxy: `%s` → `%s` (upstream `%s`)", info.Server, info.ProxyURL, info.Upstream))
	}
	for _, row := range r.Rows {
		if !rowHasContent(row) {
			continue
		}
		b.findingHeader(row.Name, row.Model)
		if text := strings.TrimSpace(row.Result); text != "" {
			b.heading(3, "Findings")
			b.richBlock(text)
		}
		if len(row.ToolCallLog) > 0 {
			b.heading(3, "Tool calls")
			b.writeToolCallTable(row.ToolCallLog)
		}
		if len(row.KubectlAPILog) > 0 {
			b.heading(3, "Kubectl network log")
			b.renderTable(buildAPILogTable(row.KubectlAPILog))
		}
		if len(row.MCPAPILog) > 0 {
			b.heading(3, "MCP network log")
			b.renderTable(buildMCPAPILogTable(row.MCPAPILog))
		}
		if row.KubectlLogPath != "" {
			b.paragraph(fmt.Sprintf("Raw kubectl log: `%s`", row.KubectlLogPath))
		}
		if row.MCPLogPath != "" {
			b.paragraph(fmt.Sprintf("Raw MCP log: `%s`", row.MCPLogPath))
		}
	}
}

func anyRunHasContent(r *Result) bool {
	for _, row := range r.Rows {
		if rowHasContent(row) {
			return true
		}
	}
	return false
}

func rowHasContent(row Row) bool {
	if strings.TrimSpace(row.Result) != "" {
		return true
	}
	if len(row.ToolCallLog) > 0 || len(row.KubectlAPILog) > 0 || len(row.MCPAPILog) > 0 {
		return true
	}
	return false
}

// writeToolCallTable renders the per-call log: each call gets a row with
// timestamp, the call (bash command for Bash, tool name otherwise), tokens,
// duration, and (for kubectl) the count of correlated proxy API calls. In
// HTML and markdown the row is a <details> that expands to show the
// tool_result body; in ANSI the body is dumped under the table.
func (b *reportBuf) writeToolCallTable(entries []ToolCallEntry) {
	switch b.format {
	case FormatHTML:
		b.writeToolCallHTML(entries)
	case FormatANSI:
		b.writeToolCallANSI(entries)
	default:
		b.writeToolCallMarkdown(entries)
	}
}

var toolCallHeaders = []string{"Time", "Command", "Tokens", "Duration", "Net"}

func (b *reportBuf) writeToolCallHTML(entries []ToolCallEntry) {
	b.raw(`<div class="tool-calls">` + "\n")
	b.raw(`<div class="tc-row tc-header"><span>Time</span><span>Command</span><span>Tokens</span><span>Duration</span><span>Net</span></div>` + "\n")
	for _, e := range entries {
		b.writef(`<details class="tc-row tc-call"><summary class="tc-summary"><span class="tc-time">%s</span><span class="tc-cmd"><code>%s</code></span><span class="tc-tokens">%s</span><span class="tc-dur">%s</span><span class="tc-net">%s</span></summary>`,
			e.Time.Local().Format("15:04:05.000"),
			html.EscapeString(truncate(e.Label(), 200)),
			html.EscapeString(formatTokens(e.InputTokens, e.OutputTokens)),
			html.EscapeString(e.Duration),
			html.EscapeString(formatNet(e)),
		)
		b.writef(`<pre class="tc-output"><code>%s</code></pre>`, html.EscapeString(e.Output))
		b.raw("</details>\n")
	}
	b.raw("</div>\n\n")
}

func (b *reportBuf) writeToolCallMarkdown(entries []ToolCallEntry) {
	b.raw("| Time | Command | Tokens | Duration | Net |\n")
	b.raw("|---|---|---|---|---|\n")
	for _, e := range entries {
		b.writef("| %s | `%s` | %s | %s | %s |\n",
			e.Time.Local().Format("15:04:05.000"),
			mdTableEscape(truncate(e.Label(), 120)),
			formatTokens(e.InputTokens, e.OutputTokens),
			e.Duration,
			formatNet(e),
		)
	}
	b.raw("\n")
	for i, e := range entries {
		if strings.TrimSpace(e.Output) == "" {
			continue
		}
		b.writef("<details><summary>%d. %s — <code>%s</code></summary>\n\n```\n%s\n```\n\n</details>\n\n",
			i+1, e.Time.Local().Format("15:04:05.000"), truncate(e.Label(), 120), e.Output)
	}
}

func (b *reportBuf) writeToolCallANSI(entries []ToolCallEntry) {
	t := api.TextTable{Headers: headerList(toolCallHeaders), FieldNames: toolCallHeaders}
	for _, e := range entries {
		t.Rows = append(t.Rows, toTableRow(map[string]string{
			"Time":     e.Time.Local().Format("15:04:05.000"),
			"Command":  truncate(e.Label(), 80),
			"Tokens":   formatTokens(e.InputTokens, e.OutputTokens),
			"Duration": e.Duration,
			"Net":      formatNet(e),
		}))
	}
	b.writef("%s\n", t.ANSI())
	for i, e := range entries {
		if strings.TrimSpace(e.Output) == "" {
			continue
		}
		b.writef("\n  %d. %s — %s\n%s\n",
			i+1, e.Time.Local().Format("15:04:05.000"), truncate(e.Label(), 80),
			indentLines(e.Output, "    "))
	}
	b.raw("\n")
}

func formatTokens(in, out int) string {
	if in == 0 && out == 0 {
		return "-"
	}
	return fmt.Sprintf("%s / %s", compactNum(in), compactNum(out))
}

func compactNum(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1_000_000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
}

func formatNet(e ToolCallEntry) string {
	if !e.IsKubectl && !e.IsMCPProxy {
		return "-"
	}
	return fmt.Sprintf("%d", e.NetworkRequests)
}

func mdTableEscape(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "|", `\|`)
}

// findingHeader emits a visually prominent header for each run's findings
// section — name as the lead, model as a subtle subtitle.
func (b *reportBuf) findingHeader(name, model string) {
	switch b.format {
	case FormatHTML:
		b.writef(`<div class="finding-header"><span class="finding-name">%s</span>`, html.EscapeString(name))
		if model != "" {
			b.writef(`<span class="finding-model">%s</span>`, html.EscapeString(model))
		}
		b.raw("</div>\n")
	case FormatANSI:
		title := name
		if model != "" {
			title = fmt.Sprintf("%s  (%s)", name, model)
		}
		b.writef("\n%s\n%s\n\n", title, strings.Repeat("━", len(title)))
	default:
		if model != "" {
			b.writef("## `%s` — %s\n\n", name, model)
		} else {
			b.writef("## `%s`\n\n", name)
		}
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
}

var apiLogHeaders = []string{"Time", "Method", "URL", "Status", "Duration", "Size"}
var mcpAPILogHeaders = []string{"Time", "Server", "Operation", "Method", "URL", "Status", "Duration", "Size"}

func buildAPILogTable(entries []KubectlAPIEntry) api.TextTable {
	t := api.TextTable{
		Headers:    headerList(apiLogHeaders),
		FieldNames: apiLogHeaders,
	}
	for _, e := range entries {
		t.Rows = append(t.Rows, toTableRow(map[string]string{
			"Time":     e.Time.Local().Format("15:04:05.000"),
			"Method":   e.Method,
			"URL":      e.URL,
			"Status":   fmt.Sprintf("%d", e.Status),
			"Duration": e.Duration,
			"Size":     humanBytes(e.Bytes),
		}))
	}
	return t
}

func buildMCPAPILogTable(entries []MCPAPIEntry) api.TextTable {
	t := api.TextTable{
		Headers:    headerList(mcpAPILogHeaders),
		FieldNames: mcpAPILogHeaders,
	}
	for _, e := range entries {
		op := e.Operation()
		if op == "" {
			op = "-"
		}
		t.Rows = append(t.Rows, toTableRow(map[string]string{
			"Time":      e.Time.Local().Format("15:04:05.000"),
			"Server":    e.Server,
			"Operation": op,
			"Method":    e.Method,
			"URL":       e.URL,
			"Status":    fmt.Sprintf("%d", e.Status),
			"Duration":  e.Duration,
			"Size":      humanBytes(e.Bytes),
		}))
	}
	return t
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
