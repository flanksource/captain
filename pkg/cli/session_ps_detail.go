package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/clicky/api"
)

const (
	// psDetailLabelWidth aligns the value column across every row of a block.
	// One wider than the longest label ("Environment") so values never abut it.
	psDetailLabelWidth = 12
	// psDetailPathWidth elides the middle of longer paths; claude transcript
	// paths embed the whole slugified project directory and blow past any
	// terminal otherwise.
	psDetailPathWidth = 58
	// psContextBarCells is the width of the context headroom bar.
	psContextBarCells = 10
	// psDetailEnvKeys is how many environment variables the terminal shows
	// before collapsing the rest into a "+N more" count. The full map is
	// always present in --json.
	psDetailEnvKeys = 6
	// psDetailEnvValueWidth truncates environment values: some carry socket
	// capability tokens that should not land in terminal scrollback.
	psDetailEnvValueWidth = 44
	psDetailCommandWidth  = 96
)

// psInspectionText renders `captain ps <pid>` as one detail block per inspected
// process. Inspection output is vertical: a named process has far more to say
// than a table row can hold, and there are only ever a handful of rows.
func psInspectionText(result PSResult) api.Text {
	t := api.Text{}
	for i, row := range result.Sessions {
		if i > 0 {
			t = t.NewLine()
		}
		t = t.Add(psDetailBlock(row)).NewLine()
	}
	return t
}

// psDetailBlock renders one inspected process: an identifying header, then the
// rows that have something to say. Rows are appended conditionally rather than
// built as a fixed list so a non-agent PID degrades to what it actually has
// instead of a column of empty labels.
func psDetailBlock(r PSRow) api.Text {
	t := api.Text{}.Add(psDetailHeader(r))
	if title := psTitle(r); title != "" {
		t = t.NewLine().Append("  ").Append(title, "font-medium")
	}
	t = t.NewLine()

	for _, row := range psDetailRows(r) {
		t = t.NewLine().Add(row)
	}
	for _, h := range r.Health {
		t = t.NewLine().Append("  ").Add(psHealthIcon(h.Severity)).Space().
			Append(h.Message, psHealthStyle(h.Severity))
	}
	return t
}

// psDetailHeader is the "● claude · claude-opus-5 · sleeping · PID 22979" line.
func psDetailHeader(r PSRow) api.Text {
	t := api.Text{}.Add(psStatusIcon(r)).Space().Add(psSourceText(r.Source))
	if r.Model != "" {
		t = psDetailSep(t).Append(r.Model, "text-purple-600 font-medium")
	}
	if r.Live != nil && r.Live.Status != "" {
		t = psDetailSep(t).Append(r.Live.Status, "text-muted")
	}
	if r.Live != nil && r.Live.PID > 0 {
		t = psDetailSep(t).Append(fmt.Sprintf("PID %d", r.Live.PID), "text-muted")
	}
	return t
}

func psDetailSep(t api.Text) api.Text {
	return t.Append(" · ", "text-gray-400")
}

// psDetailRows builds the label/value rows in display order, skipping every row
// whose value is empty.
func psDetailRows(r PSRow) []api.Text {
	var rows []api.Text
	add := func(label string, value api.Textable) {
		if value == nil || strings.TrimSpace(value.String()) == "" {
			return
		}
		rows = append(rows, psDetailRow(label, value))
	}

	add("Session", api.Text{}.Append(psSessionID(r)))
	add("Project", psDetailProject(r))
	add("Context", psContextBar(r.Context))
	add("Usage", psDetailUsage(r))
	if breakdown := psTokenBreakdown(r.Tokens); breakdown != nil {
		rows = append(rows, psDetailRow("", breakdown))
	}
	add("Volume", psVolumeLine(r))

	if r.Live != nil {
		add("Process", psProcessLine(r.Live))
		if uptime := psUptimeLine(r.Live); uptime != nil {
			rows = append(rows, psDetailRow("", uptime))
		}
		add("Cwd", api.Text{}.Append(psShortenPath(psDetailCWD(r)), "text-muted"))
		add("Command", api.Text{}.Append(psTruncate(r.Live.Command, psDetailCommandWidth), "text-muted"))
		add("Cmux", psCmuxLine(r.Live.Surface))
		add("Transcript", api.Text{}.Append(psShortenPath(r.Live.SessionFile), "text-muted"))
	}
	if agents := psAgentIDs(r); len(agents) > 0 {
		rows = append(rows, psDetailRow("Sub-agents", api.CompactList(agents)))
	}
	rows = append(rows, psEnvironmentRows(r.Live)...)
	return rows
}

// psDetailRow pads the label so every value in a block starts at the same
// column. An empty label continues the previous row's value.
func psDetailRow(label string, value api.Textable) api.Text {
	padded := label
	if padded != "" {
		padded = fmt.Sprintf("%-*s", psDetailLabelWidth, label)
	} else {
		padded = strings.Repeat(" ", psDetailLabelWidth)
	}
	return api.Text{}.Append("  ").Append(padded, "text-muted").Add(value)
}

func psDetailProject(r PSRow) api.Textable {
	name := sessionProjectName(r.SessionRecord)
	if name == "" {
		return nil
	}
	t := api.Text{}.Append(name)
	if r.GitBranch != "" {
		t = t.Append(fmt.Sprintf(" (%s)", r.GitBranch), "text-muted")
	}
	return t
}

func psDetailUsage(r PSRow) api.Textable {
	total := sessionTokenTotal(r.SessionRecord)
	if total <= 0 && r.CostUSD <= 0 {
		return nil
	}
	t := api.Text{}
	if total > 0 {
		t = t.Add(api.HumanNumber(int64(total))).Append(" tokens", "text-muted")
	}
	if r.CostUSD > 0 {
		if total > 0 {
			t = t.Append("  ")
		}
		t = t.Append(fmt.Sprintf("$%.2f", r.CostUSD), "text-green-600 font-medium")
	}
	return t
}

// psTokenBreakdown is the "in 264 · out 73.2K · cache r 21M / w 211K" line that
// explains the token total.
func psTokenBreakdown(tokens *SessionTokensWire) api.Textable {
	if tokens == nil {
		return nil
	}
	var parts []api.Text
	if tokens.InputTokens > 0 {
		parts = append(parts, api.Text{}.Append("in ", "text-muted").Add(api.HumanNumber(int64(tokens.InputTokens))))
	}
	if tokens.OutputTokens > 0 {
		parts = append(parts, api.Text{}.Append("out ", "text-muted").Add(api.HumanNumber(int64(tokens.OutputTokens))))
	}
	if tokens.CacheReadTokens > 0 || tokens.CacheCreationTokens > 0 {
		cache := api.Text{}.Append("cache r ", "text-muted").Add(api.HumanNumber(int64(tokens.CacheReadTokens)))
		cache = cache.Append(" / w ", "text-muted").Add(api.HumanNumber(int64(tokens.CacheCreationTokens)))
		parts = append(parts, cache)
	}
	return psJoinDotted(parts)
}

func psVolumeLine(r PSRow) api.Textable {
	var parts []api.Text
	if r.Messages > 0 {
		parts = append(parts, api.Text{}.Add(api.HumanNumber(int64(r.Messages))).Append(" messages", "text-muted"))
	}
	if r.ToolCalls > 0 {
		parts = append(parts, api.Text{}.Add(api.HumanNumber(int64(r.ToolCalls))).Append(" tool calls", "text-muted"))
	}
	return psJoinDotted(parts)
}

// psProcessLine is the live resource cost: "ppid 21271 · cpu 15.9% · rss 30.7MB".
func psProcessLine(live *SessionLiveWire) api.Textable {
	var parts []api.Text
	if live.PPID > 0 {
		parts = append(parts, api.Text{}.Append("ppid ", "text-muted").Appendf("%d", live.PPID))
	}
	if live.CPUPercent > 0 {
		parts = append(parts, api.Text{}.Append("cpu ", "text-muted").Appendf("%.1f%%", live.CPUPercent))
	}
	if live.RSSBytes > 0 {
		parts = append(parts, api.Text{}.Append("rss ", "text-muted").Add(api.HumanizeBytes(int64(live.RSSBytes))))
	} else if live.MemoryPercent > 0 {
		parts = append(parts, api.Text{}.Append("mem ", "text-muted").Appendf("%.1f%%", live.MemoryPercent))
	}
	return psJoinDotted(parts)
}

// psUptimeLine reports how long the process has been running, which the table's
// last-activity column does not answer.
func psUptimeLine(live *SessionLiveWire) api.Textable {
	if live.StartedAt == nil {
		return nil
	}
	// A local wall-clock stamp, not api.Human: it renders a non-UTC time as
	// RFC3339, which is unreadable next to a humanised duration.
	return api.Text{}.
		Append("up ", "text-muted").Add(api.Human(time.Since(*live.StartedAt))).
		Append(" · ", "text-gray-400").
		Append("started ", "text-muted").
		Append(live.StartedAt.Local().Format("2006-01-02 15:04"))
}

func psCmuxLine(surface *CmuxSurface) api.Textable {
	if surface == nil {
		return nil
	}
	var parts []api.Text
	if surface.Workspace != "" {
		parts = append(parts, api.Text{}.Append(surface.Workspace))
	}
	if ref := surface.SurfaceRef; ref != "" {
		parts = append(parts, api.Text{}.Append(ref, "text-muted"))
	} else if surface.SurfaceID != "" {
		parts = append(parts, api.Text{}.Append(shortSessionID(surface.SurfaceID), "text-muted"))
	}
	if surface.Port > 0 {
		parts = append(parts, api.Text{}.Append("port ", "text-muted").Appendf("%d", surface.Port))
	}
	return psJoinDotted(parts)
}

// psContextBar renders remaining context headroom as "76% free ████████░░
// 238K / 1M", escalating green → amber → red as the window fills. Clicky has no
// bar/gauge primitive, so the fill is drawn here.
func psContextBar(context *SessionContextWire) api.Textable {
	if context == nil || context.WindowTokens <= 0 {
		return nil
	}
	free := context.FreePercent
	if free < 0 {
		free = 0
	}
	if free > 100 {
		free = 100
	}
	filled := (free*psContextBarCells + 50) / 100
	bar := strings.Repeat("█", filled) + strings.Repeat("░", psContextBarCells-filled)
	return api.Text{}.
		Appendf("%d%%", free).Append(" free", "text-muted").Append("  ").
		Append(bar, psContextBarStyle(free)).Append("  ").
		Add(api.HumanNumber(int64(context.UsedTokens))).
		Append(" / ", "text-muted").
		Add(api.HumanNumber(int64(context.WindowTokens)))
}

// psEnvironmentRows shows the session-bearing variables and collapses the rest
// into a count. The terminal deliberately gets a summary rather than the whole
// map: an inspected process can carry 130+ variables, some of them credentials.
func psEnvironmentRows(live *SessionLiveWire) []api.Text {
	if live == nil || len(live.Environment) == 0 {
		return nil
	}
	interesting, others := psSplitEnvironmentKeys(live.Environment)
	if len(interesting) == 0 {
		return []api.Text{psDetailRow("Environment", api.Text{}.
			Appendf("%d variables", len(others)).Styles("text-muted"))}
	}

	shown := interesting
	hidden := len(others)
	if len(shown) > psDetailEnvKeys {
		hidden += len(shown) - psDetailEnvKeys
		shown = shown[:psDetailEnvKeys]
	}

	rows := make([]api.Text, 0, len(shown))
	for i, key := range shown {
		label := ""
		if i == 0 {
			label = "Environment"
		}
		value := api.Text{}.Append(key, "text-muted").Append("=").
			Append(psTruncate(live.Environment[key], psDetailEnvValueWidth))
		rows = append(rows, psDetailRow(label, value))
	}
	if hidden > 0 {
		rows = append(rows, psDetailRow("", api.Text{}.Appendf("(+%d more)", hidden).Styles("text-muted")))
	}
	return rows
}

// psContextBarStyle escalates as the context window fills: a session with room
// left reads green, one about to compact reads red.
func psContextBarStyle(free int) string {
	switch {
	case free >= 50:
		return "text-green-500"
	case free >= 20:
		return "text-amber-500"
	default:
		return "text-red-500"
	}
}

func psSplitEnvironmentKeys(environment map[string]string) (interesting, others []string) {
	for key := range environment {
		if hasAnyPrefix(key, psInterestingEnvPrefixes) {
			interesting = append(interesting, key)
		} else {
			others = append(others, key)
		}
	}
	sort.Strings(interesting)
	sort.Strings(others)
	return interesting, others
}

func psDetailCWD(r PSRow) string {
	if r.CWD != "" {
		return r.CWD
	}
	if r.Live != nil {
		return r.Live.CWD
	}
	return ""
}

func psJoinDotted(parts []api.Text) api.Textable {
	if len(parts) == 0 {
		return nil
	}
	t := api.Text{}
	for i, part := range parts {
		if i > 0 {
			t = t.Append(" · ", "text-gray-400")
		}
		t = t.Add(part)
	}
	return t
}

// psShortenPath replaces the home directory with "~" and elides interior
// directories, so a long transcript path still shows where it starts and which
// file it names.
func psShortenPath(path string) string {
	if path == "" {
		return ""
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, relErr := filepath.Rel(home, path); relErr == nil && !strings.HasPrefix(rel, "..") {
			path = filepath.Join("~", rel)
		}
	}
	if len([]rune(path)) <= psDetailPathWidth {
		return path
	}
	segments := strings.Split(path, string(filepath.Separator))
	for lead := len(segments) - 2; lead >= 1; lead-- {
		elided := append(append([]string{}, segments[:lead]...), "…", segments[len(segments)-1])
		joined := strings.Join(elided, string(filepath.Separator))
		if len([]rune(joined)) <= psDetailPathWidth {
			return joined
		}
	}
	return filepath.Base(path)
}

func psTruncate(value string, width int) string {
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	return string(runes[:width-1]) + "…"
}
