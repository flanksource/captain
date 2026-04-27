package fixture

import (
	"bytes"
	"strings"
	"testing"
)

func sampleResult() *Result {
	tru := true
	f := &Fixture{
		Name:        "mc-bench",
		Description: "MC vs direct",
		Prompt:      "Which cluster is unhealthy?",
		Baseline:    "direct",
		Runs: []Run{
			{Name: "direct", Model: "sonnet-4", Tools: []string{"Bash"}},
			{Name: "mcp", Model: "sonnet-4", PromptCaching: &tru, MCPConfig: []string{".mcp.json"}},
		},
	}
	return &Result{
		Name:        f.Name,
		Description: f.Description,
		Baseline:    "direct",
		Fixture:     f,
		Rows: []Row{
			{Name: "direct", Model: "sonnet-4", Repeat: 1, DurationMS: "4s", CostUSD: "$0.0800",
				DurationMeanMS: 4000, CostMeanUSD: 0.08, ToolCalls: 2, BashCalls: 2,
				ToolSummary: "Bash:2", Speedup: "1.00x", Cheaper: "1.00x", Status: "OK"},
			{Name: "mcp", Model: "sonnet-4", Repeat: 1, DurationMS: "1s", CostUSD: "$0.0100",
				DurationMeanMS: 1000, CostMeanUSD: 0.01, ToolCalls: 1, MCPCalls: 1,
				ToolSummary: "mcp__mc__query:1", Speedup: "4.00x", Cheaper: "8.00x", Status: "OK"},
		},
	}
}

func TestWriteReport_Markdown(t *testing.T) {
	r := sampleResult()
	var buf bytes.Buffer
	if err := WriteReport(&buf, r, FormatMarkdown); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	wants := []string{
		"# mc-bench",
		"MC vs direct",
		"## Headline",
		"4.00x faster",
		"8.00x cheaper",
		"## Metrics",
		"direct",
		"mcp",
		"## Run configurations",
		"### `direct`",
		"### `mcp`",
		"Prompt caching: `true`",
		"Which cluster is unhealthy?",
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("markdown missing %q\n---\n%s", w, out)
		}
	}
}

func TestWriteReport_HTML(t *testing.T) {
	r := sampleResult()
	var buf bytes.Buffer
	if err := WriteReport(&buf, r, FormatHTML); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	wants := []string{
		"<h1>mc-bench</h1>",
		"MC vs direct",
		"<h2>Headline</h2>",
		"4.00x faster",
		"<h2>Metrics</h2>",
		"<table",
		"direct",
		"mcp",
		"<h2>Run configurations</h2>",
		"Which cluster is unhealthy?",
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("html missing %q\n---\n%s", w, out)
		}
	}
}

func TestWriteReport_ANSI(t *testing.T) {
	r := sampleResult()
	var buf bytes.Buffer
	if err := WriteReport(&buf, r, FormatANSI); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	wants := []string{
		"mc-bench",
		"Headline",
		"4.00x faster",
		"Metrics",
		"direct",
		"mcp",
		"Which cluster is unhealthy?",
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("ansi missing %q\n---\n%s", w, out)
		}
	}
}

func TestCompareRatio_SlowerAndPricier(t *testing.T) {
	if got := compareRatio(1000, 2000, "faster", "slower"); got != "2.00x slower" {
		t.Errorf("got %q, want 2.00x slower", got)
	}
	if got := compareRatio(0.01, 0.04, "cheaper", "pricier"); got != "4.00x pricier" {
		t.Errorf("got %q, want 4.00x pricier", got)
	}
	if got := compareRatio(0, 1, "faster", "slower"); got != "n/a" {
		t.Errorf("got %q, want n/a", got)
	}
}
