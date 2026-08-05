package session

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/flanksource/captain/pkg/claude/tools"
	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
)

// RowOptions carries the per-row render toggles the history table honors,
// decoupling the render provider from the CLI's flag struct.
type RowOptions struct {
	Cost bool // include per-model token/cost breakdown in row detail + the Cost column
	Raw  bool // include the raw JSONL line in row detail and merge it into JSON output
}

// FormatTokens renders a token count compactly (1.2M / 3.4K / 42).
func FormatTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// FormatCost renders a USD cost, using 4 decimals for sub-cent amounts.
func FormatCost(cost float64) string {
	if cost < 0.01 {
		return fmt.Sprintf("$%.4f", cost)
	}
	return fmt.Sprintf("$%.2f", cost)
}

// FormatCostEstimated renders a cost, marking it when the figure was recomputed
// from token counts rather than reported by the provider.
//
// The two must be visually distinct. A recomputed total is priced from a static
// registry, so it silently misses whatever the provider billed but never
// published — 1-hour cache writes, promotional rates, negotiated pricing. On a
// real session the recomputation came out ~9% under the billed figure, and
// nothing in the output said so.
func FormatCostEstimated(cost float64, estimated bool) string {
	if estimated && cost > 0 {
		return "~" + FormatCost(cost)
	}
	return FormatCost(cost)
}

// ScanResultRow is a history row for the --all view (includes the Project column).
type ScanResultRow struct {
	Project         string          `json:"project"`
	Tool            string          `json:"tool"`
	Summary         string          `json:"summary"`
	Subject         api.Textable    `json:"-"`
	Detail          api.Textable    `json:"-"`
	Paths           string          `json:"paths,omitempty"`
	ReadPaths       []string        `json:"readPaths,omitempty"`
	WritePaths      []string        `json:"writePaths,omitempty"`
	BinariesDisplay string          `json:"binariesDisplay,omitempty"`
	Binaries        []string        `json:"binaries,omitempty"`
	Category        string          `json:"category"`
	Approved        string          `json:"approved,omitempty"`
	Agent           string          `json:"agent,omitempty"` // sub-agent attribution for sidechain rows
	Time            string          `json:"time"`
	Cost            string          `json:"cost,omitempty"`
	Raw             json.RawMessage `json:"-"` // surfaced via MarshalJSON when --raw is set
}

func (r ScanResultRow) MarshalJSON() ([]byte, error) {
	type alias ScanResultRow
	return mergeRowWithRaw(alias(r), r.Raw)
}

func (r ScanResultRow) Columns() []api.ColumnDef {
	cols := []api.ColumnDef{
		api.Column("time").Label("Time").Build(),
		api.Column("project").Label("Project").Build(),
		api.Column("tool").Label("Tool").Build(),
		api.Column("subject").Label("Subject").Build(),
		api.Column("category").Label("Category").Build(),
		api.Column("approved").Label("Approved").Build(),
	}
	if r.Agent != "" {
		cols = append(cols, api.Column("agent").Label("Agent").Build())
	}
	if r.Cost != "" {
		cols = append(cols, api.Column("cost").Label("Cost").Build())
	}
	return cols
}

func (r ScanResultRow) Row() map[string]any {
	return map[string]any{
		"time":     r.Time,
		"project":  r.Project,
		"tool":     r.Tool,
		"subject":  r.Subject,
		"category": r.Category,
		"approved": r.Approved,
		"agent":    r.Agent,
		"cost":     r.Cost,
	}
}

func (r ScanResultRow) RowDetail() api.Textable {
	return r.Detail
}

// ScanResultRowSingle is a history row for the single-project view (no Project column).
type ScanResultRowSingle struct {
	Tool            string          `json:"tool"`
	Summary         string          `json:"summary"`
	Subject         api.Textable    `json:"-"`
	Detail          api.Textable    `json:"-"`
	Paths           string          `json:"paths,omitempty"`
	ReadPaths       []string        `json:"readPaths,omitempty"`
	WritePaths      []string        `json:"writePaths,omitempty"`
	BinariesDisplay string          `json:"binariesDisplay,omitempty"`
	Binaries        []string        `json:"binaries,omitempty"`
	Category        string          `json:"category"`
	Approved        string          `json:"approved,omitempty"`
	Agent           string          `json:"agent,omitempty"` // sub-agent attribution for sidechain rows
	Time            string          `json:"time"`
	Cost            string          `json:"cost,omitempty"`
	Raw             json.RawMessage `json:"-"` // surfaced via MarshalJSON when --raw is set
}

func (r ScanResultRowSingle) MarshalJSON() ([]byte, error) {
	type alias ScanResultRowSingle
	return mergeRowWithRaw(alias(r), r.Raw)
}

// mergeRowWithRaw marshals the row's standard fields, then if raw is non-empty
// merges its top-level keys into the resulting JSON object. Standard row
// fields take precedence on conflict so the row's category/tool/etc. don't
// get overwritten by raw fields with the same name.
func mergeRowWithRaw(row any, raw json.RawMessage) ([]byte, error) {
	rowJSON, err := json.Marshal(row)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return rowJSON, nil
	}
	var rowMap map[string]json.RawMessage
	if err := json.Unmarshal(rowJSON, &rowMap); err != nil {
		return rowJSON, nil
	}
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		// Raw isn't a JSON object — surface it under a "raw" key.
		rowMap["raw"] = raw
		return json.Marshal(rowMap)
	}
	for k, v := range rawMap {
		if _, exists := rowMap[k]; !exists {
			rowMap[k] = v
		}
	}
	return json.Marshal(rowMap)
}

func (r ScanResultRowSingle) Columns() []api.ColumnDef {
	cols := []api.ColumnDef{
		api.Column("time").Label("Time").Build(),
		api.Column("tool").Label("Tool").Build(),
		api.Column("subject").Label("Subject").Build(),
		api.Column("category").Label("Category").Build(),
		api.Column("approved").Label("Approved").Build(),
	}
	if r.Agent != "" {
		cols = append(cols, api.Column("agent").Label("Agent").Build())
	}
	if r.Cost != "" {
		cols = append(cols, api.Column("cost").Label("Cost").Build())
	}
	return cols
}

func (r ScanResultRowSingle) Row() map[string]any {
	return map[string]any{
		"time":     r.Time,
		"tool":     r.Tool,
		"subject":  r.Subject,
		"category": r.Category,
		"approved": r.Approved,
		"agent":    r.Agent,
		"cost":     r.Cost,
	}
}

func (r ScanResultRowSingle) RowDetail() api.Textable {
	return r.Detail
}

// HistoryResultAll is the --all history view (summary + project-tagged rows).
type HistoryResultAll struct {
	Total        int             `json:"total" pretty:"label=Total"`
	UserDenied   int             `json:"userDenied,omitempty" pretty:"label=User Denied"`
	Duration     string          `json:"duration,omitempty" pretty:"label=Duration"`
	Tokens       int             `json:"tokens,omitempty" pretty:"label=Tokens"`
	InputTokens  string          `json:"inputTokens,omitempty" pretty:"label=Input"`
	OutputTokens string          `json:"outputTokens,omitempty" pretty:"label=Output"`
	CacheRead    string          `json:"cacheRead,omitempty" pretty:"label=Cache Read"`
	CacheWrite   string          `json:"cacheWrite,omitempty" pretty:"label=Cache Write"`
	Cost         string          `json:"cost,omitempty" pretty:"label=Cost"`
	Results      []ScanResultRow `json:"results"`
}

// HistoryResult is the single-project history view.
type HistoryResult struct {
	Project      string                `json:"project" pretty:"label=Project"`
	Total        int                   `json:"total" pretty:"label=Total"`
	UserDenied   int                   `json:"userDenied,omitempty" pretty:"label=User Denied"`
	Duration     string                `json:"duration,omitempty" pretty:"label=Duration"`
	Tokens       int                   `json:"tokens,omitempty" pretty:"label=Tokens"`
	InputTokens  string                `json:"inputTokens,omitempty" pretty:"label=Input"`
	OutputTokens string                `json:"outputTokens,omitempty" pretty:"label=Output"`
	CacheRead    string                `json:"cacheRead,omitempty" pretty:"label=Cache Read"`
	CacheWrite   string                `json:"cacheWrite,omitempty" pretty:"label=Cache Write"`
	Cost         string                `json:"cost,omitempty" pretty:"label=Cost"`
	Results      []ScanResultRowSingle `json:"results"`
}

// BuildRowDetail composes the row-detail Textable rendered by the table view.
// The base detail (denial reasons etc.) is rendered first, followed by an
// optional cost breakdown (--cost) and raw JSONL line (--raw).
func BuildRowDetail(t tools.Tool, opts RowOptions) api.Textable {
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

// RowCost returns the formatted total cost for a row when --cost is set, or the
// empty string otherwise (so the Cost column hides itself).
func RowCost(b *tools.BaseTool, opts RowOptions) string {
	if !opts.Cost || b == nil || len(b.Models) == 0 {
		return ""
	}
	cost := b.Models.TotalCost()
	if cost == 0 {
		return ""
	}
	return FormatCost(cost)
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
			Add(api.Badge("In:"+FormatTokens(b.Models.TotalInput()), "bg-blue-100")).
			Add(api.Badge("Out:"+FormatTokens(b.Models.TotalOutput()), "bg-purple-100")).
			Add(api.Badge("Cache:"+FormatTokens(b.Models.TotalCacheRead()), "bg-amber-100")).
			Add(api.Badge(FormatCost(b.Models.TotalCost()), "bg-green-100"))
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
		section = section.Add(api.Badge("In:"+FormatTokens(m.InputTokens), "bg-blue-100"))
	}
	if m.OutputTokens > 0 {
		section = section.Add(api.Badge("Out:"+FormatTokens(m.OutputTokens), "bg-purple-100"))
	}
	if m.CacheReadInputTokens > 0 {
		section = section.Add(api.Badge("CacheRead:"+FormatTokens(m.CacheReadInputTokens), "bg-amber-100"))
	}
	if m.CacheCreationInputTokens > 0 {
		section = section.Add(api.Badge("CacheWrite:"+FormatTokens(m.CacheCreationInputTokens), "bg-amber-200"))
	}
	if m.Cost > 0 {
		section = section.Add(api.Badge(FormatCost(m.Cost), "bg-green-100"))
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
