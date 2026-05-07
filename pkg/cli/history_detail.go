package cli

import (
	"bytes"
	"encoding/json"

	"github.com/flanksource/captain/pkg/claude/tools"
	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
)

// buildRowDetail composes the row-detail Textable rendered by the table view.
// The base detail (denial reasons etc.) is rendered first, followed by an
// optional cost breakdown (--cost) and raw JSONL line (--raw).
func buildRowDetail(t tools.Tool, opts HistoryOptions) api.Textable {
	base := t.Detail()
	if !opts.Cost && !opts.Raw {
		return base
	}

	out := clicky.Text("")
	first := true
	addTextable := func(child api.Textable) {
		if !first {
			out = out.Append("\n")
		}
		out = out.Add(child)
		first = false
	}
	addText := func(section api.Text) {
		addTextable(&section)
	}

	if base != nil {
		addTextable(base)
	}
	if opts.Cost {
		if section, ok := costSection(t.Base()); ok {
			addText(section)
		}
	}
	if opts.Raw {
		if section, ok := rawSection(t.Base()); ok {
			addText(section)
		}
	}

	if first {
		return base
	}
	return &out
}

// rowCost returns the formatted total cost for a row when --cost is set,
// or the empty string otherwise (so the Cost column hides itself).
func rowCost(b *tools.BaseTool, opts HistoryOptions) string {
	if !opts.Cost || b == nil || len(b.Models) == 0 {
		return ""
	}
	cost := b.Models.TotalCost()
	if cost == 0 {
		return ""
	}
	return formatCost(cost)
}

// costSection renders per-model token & cost breakdown using clicky Badges.
func costSection(b *tools.BaseTool) (api.Text, bool) {
	if b == nil || len(b.Models) == 0 {
		return clicky.Text(""), false
	}

	section := clicky.Text("").Append("Cost", "font-bold")
	for _, m := range b.Models {
		section = section.Append("\n")
		section = appendModelBadges(section, m)
	}

	if len(b.Models) > 1 {
		section = section.Append("\n")
		section = section.Append("Total", "font-bold").Append(" ")
		section = section.
			Add(api.Badge("In:"+formatTokens(b.Models.TotalInput()), "bg-blue-100")).
			Add(api.Badge("Out:"+formatTokens(b.Models.TotalOutput()), "bg-purple-100")).
			Add(api.Badge("Cache:"+formatTokens(b.Models.TotalCacheRead()), "bg-amber-100")).
			Add(api.Badge(formatCost(b.Models.TotalCost()), "bg-green-100"))
	}

	return section, true
}

func appendModelBadges(section api.Text, m tools.ModelUsage) api.Text {
	if m.Model != "" {
		section = section.Add(api.Badge(m.Model, "bg-gray-200"))
	}
	if m.ServiceTier != "" {
		section = section.Add(api.Badge(m.ServiceTier, "bg-gray-100"))
	}
	if m.InputTokens > 0 {
		section = section.Add(api.Badge("In:"+formatTokens(m.InputTokens), "bg-blue-100"))
	}
	if m.OutputTokens > 0 {
		section = section.Add(api.Badge("Out:"+formatTokens(m.OutputTokens), "bg-purple-100"))
	}
	if m.CacheReadInputTokens > 0 {
		section = section.Add(api.Badge("CacheRead:"+formatTokens(m.CacheReadInputTokens), "bg-amber-100"))
	}
	if m.CacheCreationInputTokens > 0 {
		section = section.Add(api.Badge("CacheWrite:"+formatTokens(m.CacheCreationInputTokens), "bg-amber-200"))
	}
	if m.Cost > 0 {
		section = section.Add(api.Badge(formatCost(m.Cost), "bg-green-100"))
	}
	return section
}

func rawSection(b *tools.BaseTool) (api.Text, bool) {
	if b == nil || len(b.RawEntry) == 0 {
		return clicky.Text(""), false
	}
	var buf bytes.Buffer
	body := string(b.RawEntry)
	if err := json.Indent(&buf, b.RawEntry, "", "  "); err == nil {
		body = buf.String()
	}
	section := clicky.Text("").
		Append("Raw", "font-bold").
		NewLine().
		Add(clicky.CodeBlock("json", body))
	return section, true
}
