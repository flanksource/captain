package cli

import (
	"fmt"
	"strings"

	"github.com/flanksource/clicky"
	clickyapi "github.com/flanksource/clicky/api"
	clickymarkdown "github.com/flanksource/clicky/markdown"
)

// PromptRunResult is the unified result of the "run" action. Over HTTP (serve)
// it carries the async handle; on the CLI it carries the synchronous result.
type PromptRunResult struct {
	RunID        string           `json:"runId,omitempty"`
	BatchID      string           `json:"batchId,omitempty"`
	Status       string           `json:"status,omitempty" pretty:"label=Status"`
	Model        string           `json:"model,omitempty" pretty:"label=Model"`
	Backend      string           `json:"backend,omitempty" pretty:"label=Backend"`
	Chat         bool             `json:"chat,omitempty"`
	Capabilities ChatCapabilities `json:"capabilities,omitempty"`

	Text             string         `json:"text,omitempty" pretty:"label=Response"`
	StructuredOutput map[string]any `json:"structuredOutput,omitempty" pretty:"-"`
	SessionID        string         `json:"sessionId,omitempty" pretty:"label=Session"`
	Dir              string         `json:"dir,omitempty" pretty:"label=Dir"`
	HistoryFile      string         `json:"historyFile,omitempty" pretty:"label=History"`
	InputTokens      int            `json:"inputTokens,omitempty" pretty:"label=Input Tokens"`
	OutputTokens     int            `json:"outputTokens,omitempty" pretty:"label=Output Tokens"`
	CostUSD          float64        `json:"costUSD,omitempty" pretty:"label=Cost USD"`
	Duration         string         `json:"duration,omitempty" pretty:"label=Duration"`

	Total     int             `json:"total,omitempty" pretty:"label=Total"`
	Succeeded int             `json:"succeeded,omitempty" pretty:"label=Succeeded"`
	Failed    int             `json:"failed,omitempty" pretty:"label=Failed"`
	Runs      []PromptRunItem `json:"runs,omitempty" pretty:"label=Runs"`
}

type PromptRunItem struct {
	RunID            string           `json:"runId,omitempty" pretty:"label=Run"`
	Selector         string           `json:"selector,omitempty" pretty:"label=Selector"`
	Status           string           `json:"status,omitempty" pretty:"label=Status"`
	Model            string           `json:"model,omitempty" pretty:"label=Model"`
	Backend          string           `json:"backend,omitempty" pretty:"label=Backend"`
	Effort           string           `json:"effort,omitempty" pretty:"label=Effort"`
	Chat             bool             `json:"chat,omitempty"`
	Capabilities     ChatCapabilities `json:"capabilities,omitempty"`
	Text             string           `json:"text,omitempty" pretty:"label=Response"`
	StructuredOutput map[string]any   `json:"structuredOutput,omitempty" pretty:"-"`
	SessionID        string           `json:"sessionId,omitempty" pretty:"label=Session"`
	Dir              string           `json:"dir,omitempty" pretty:"label=Dir"`
	HistoryFile      string           `json:"historyFile,omitempty" pretty:"label=History"`
	InputTokens      int              `json:"inputTokens,omitempty" pretty:"label=Input Tokens"`
	OutputTokens     int              `json:"outputTokens,omitempty" pretty:"label=Output Tokens"`
	CostUSD          float64          `json:"costUSD,omitempty" pretty:"label=Cost USD"`
	Duration         string           `json:"duration,omitempty" pretty:"label=Duration"`
	Error            string           `json:"error,omitempty" pretty:"label=Error"`
}

func (r PromptRunResult) Pretty() clickyapi.Text {
	if len(r.Runs) == 0 {
		if r.Text != "" {
			return clickyapi.Text{}.Add(promptResponseMarkdown(r.Text))
		}
		return clickyapi.Text{}.Append(r.Status)
	}
	t := clickyapi.Text{}.
		Append(fmt.Sprintf("Status: %s  Total: %d  Succeeded: %d  Failed: %d  Duration: %s",
			r.Status, r.Total, r.Succeeded, r.Failed, r.Duration), "font-medium")

	t = t.NewLine().Add(promptRunComparisonTable(r.Runs))
	for _, run := range r.Runs {
		if strings.TrimSpace(run.Text) == "" {
			continue
		}
		t = t.NewLine().NewLine().
			Append("Response — ", "text-gray-500").
			Append(runColumnHeader(run), "font-bold").
			NewLine().
			Add(promptResponseMarkdown(run.Text))
	}
	return t
}

func promptResponseMarkdown(source string) clickyapi.Textable {
	document, err := clicky.ParseMarkdown(source, clickymarkdown.WithFrontmatter(false))
	if err != nil {
		panic(fmt.Errorf("parse prompt response markdown: %w", err))
	}
	return document
}

func promptRunComparisonTable(runs []PromptRunItem) clickyapi.TextTable {
	table := clickyapi.TextTable{
		Headers:    clickyapi.TextList{textCell("Metric")},
		FieldNames: []string{"metric"},
	}
	for i, run := range runs {
		field := runColumnField(i)
		table.FieldNames = append(table.FieldNames, field)
		table.Headers = append(table.Headers, textCell(runColumnHeader(run)))
	}

	add := func(metric string, values func(PromptRunItem) string) {
		row := clickyapi.TableRow{"metric": cell(metric)}
		for i, run := range runs {
			row[runColumnField(i)] = cell(values(run))
		}
		table.Rows = append(table.Rows, row)
	}
	add("Status", func(run PromptRunItem) string { return run.Status })
	add("Backend", func(run PromptRunItem) string { return run.Backend })
	add("Model", func(run PromptRunItem) string { return run.Model })
	add("Error", func(run PromptRunItem) string { return truncateCell(run.Error, 160) })
	add("Duration", func(run PromptRunItem) string { return run.Duration })
	add("Tokens", func(run PromptRunItem) string { return tokenCell(run.InputTokens, run.OutputTokens) })
	add("Cost", func(run PromptRunItem) string { return costCell(run.CostUSD) })
	add("Session", func(run PromptRunItem) string { return shortSessionCell(run.SessionID) })
	add("History", func(run PromptRunItem) string { return truncatePathCell(run.HistoryFile, 72) })
	add("Dir", func(run PromptRunItem) string { return truncatePathCell(run.Dir, 56) })
	return table
}

func runColumnField(index int) string {
	return fmt.Sprintf("run%d", index+1)
}

func runColumnHeader(run PromptRunItem) string {
	if strings.TrimSpace(run.Selector) != "" {
		return run.Selector
	}
	if run.Backend != "" && run.Model != "" {
		return run.Backend + ":" + run.Model
	}
	return firstNonEmpty(run.Model, run.Backend, "run")
}

func textCell(s string) clickyapi.Textable {
	return clickyapi.Text{Content: s}
}

func cell(s string) clickyapi.TypedValue {
	return clickyapi.TypedValue{Textable: clickyapi.Text{Content: s}}
}

func truncateCell(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func truncatePathCell(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return "..." + s[len(s)-max+3:]
}

func shortSessionCell(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}

func tokenCell(input, output int) string {
	if input == 0 && output == 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d", input, output)
}

func costCell(cost float64) string {
	if cost <= 0 {
		return ""
	}
	return fmt.Sprintf("$%.4f", cost)
}
