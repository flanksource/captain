package cli

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
)

// PSRow is a live-session table row. It embeds SessionRecord (so JSON keeps the
// SessionRecord shape via field promotion) and implements clicky's TableProvider
// so each attribute renders in its own column — one value per cell.
type PSRow struct {
	SessionRecord
}

func (r PSRow) Columns() []api.ColumnDef {
	return []api.ColumnDef{
		api.Column("status").Label("").Build(),
		api.Column("agent").Label("Agent").MaxWidth(26).Build(),
		api.Column("title").Label("Title").MaxWidth(32).Build(),
		api.Column("project").Label("Project").MaxWidth(20).Build(),
		api.Column("session").Label("Session").MaxWidth(12).Build(),
		api.Column("pid").Label("PID").Build(),
		api.Column("cmux").Label("Cmux").MaxWidth(10).Build(),
		api.Column("usage").Label("Usage").Build(),
		api.Column("activity").Label("Activity").Build(),
	}
}

func (r PSRow) Row() map[string]any {
	row := map[string]any{
		"status":  psStatusIcon(r),
		"agent":   psAgentCell(r),
		"title":   psTitle(r),
		"session": sessionListID(psSessionID(r)),
		"pid":     psPID(r),
	}
	if r.CWD != "" {
		row["project"] = filepath.Base(r.CWD)
	}
	if r.Live != nil && r.Live.Surface != nil && r.Live.Surface.SurfaceID != "" {
		row["cmux"] = shortSessionID(r.Live.Surface.SurfaceID)
	}
	if usage := psUsageCell(r); usage != nil {
		row["usage"] = usage
	}
	if r.Live != nil && r.Live.LastActivity != nil {
		row["activity"] = api.Human(time.Since(*r.Live.LastActivity), "text-muted")
	}
	return row
}

// psAgentCell merges source, model, and sub-agent count into one cell:
// "codex gpt-5.6-sol +1".
func psAgentCell(r PSRow) api.Text {
	t := psSourceText(r.Source)
	if r.Model != "" {
		t = t.Space().Append(r.Model, "text-muted")
	}
	if n := psAgentCount(r); n > 0 {
		t = t.Append(fmt.Sprintf(" +%d", n), "text-blue-500")
	}
	return t
}

// psUsageCell merges token total and cost into one cell: "27.4M $13.61".
// Returns nil when the session has neither (a fresh/synthetic process).
func psUsageCell(r PSRow) api.Textable {
	return sessionUsageCell(r.SessionRecord)
}

// RowDetail expands the verbose fields that don't belong in the scannable table:
// full identifiers, working dir, command, the full CMUX surface, sub-agent ids,
// context headroom, and any health signals.
func (r PSRow) RowDetail() api.Textable {
	items := []api.KeyValuePair{
		api.KeyValue("Session", psSessionID(r)),
		api.KeyValue("CWD", r.CWD),
	}
	if r.Live != nil {
		items = append(items, api.KeyValue("Command", compactSessionCommand(r.Live.Command)))
		if s := r.Live.Surface; s != nil {
			items = append(items,
				api.KeyValue("Workspace", s.Workspace),
				api.KeyValue("Surface", s.SurfaceID),
				api.KeyValue("Tab", s.TabID),
				api.KeyValue("Panel", s.PanelID),
				api.KeyValue("Port", intOrBlank(s.Port)),
				api.KeyValue("Claude PID", intOrBlank(s.ClaudePID)),
				api.KeyValue("Socket", s.SocketPath),
			)
		}
	}
	if r.Context != nil && r.Context.FreePercent > 0 {
		items = append(items, api.KeyValue("Context", fmt.Sprintf("%d%% free", r.Context.FreePercent)))
	}

	t := api.Text{}.Add(api.DescriptionList{Items: items})
	if agents := psAgentIDs(r); len(agents) > 0 {
		t = t.NewLine().Append("Sub-agents: ", "text-muted").Add(api.CompactList(agents))
	}
	for _, h := range r.Health {
		t = t.NewLine().Add(psHealthIcon(h.Severity)).Space().Append(h.Message, psHealthStyle(h.Severity))
	}
	return t
}

// psStatusIcon is green when the process is alive and healthy, and escalates to
// warning/error on stopped/zombie processes or health signals.
func psStatusIcon(r PSRow) api.Textable {
	severity := ""
	for _, h := range r.Health {
		if h.Severity == "critical" {
			severity = "critical"
			break
		}
		if h.Severity == "warning" {
			severity = "warning"
		}
	}
	status := ""
	if r.Live != nil {
		status = strings.ToLower(r.Live.Status)
	}
	switch {
	case severity == "critical" || status == "zombie":
		return icons.Error
	case severity == "warning" || status == "stopped":
		return icons.Warning
	case status == "active" || status == "sleeping":
		return icons.Success
	default:
		return icons.Info
	}
}

// psTitle prefers the authoritative cmux surface title, falling back to the
// transcript slug when the process has no known cmux surface.
func psTitle(r PSRow) string {
	if r.Live != nil && r.Live.Surface != nil && r.Live.Surface.Title != "" {
		return r.Live.Surface.Title
	}
	return r.Slug
}

func psSourceText(source string) api.Text {
	switch source {
	case "claude":
		return api.Text{Content: "claude", Style: "text-violet-500"}
	case "codex":
		return api.Text{Content: "codex", Style: "text-cyan-600"}
	default:
		return api.Text{Content: source}
	}
}

func psHealthIcon(severity string) api.Textable {
	switch severity {
	case "critical":
		return icons.Error
	case "warning":
		return icons.Warning
	default:
		return icons.Info
	}
}

func psHealthStyle(severity string) string {
	switch severity {
	case "critical":
		return "text-red-500"
	case "warning":
		return "text-yellow-600"
	default:
		return "text-muted"
	}
}

// psPID renders the pid as a string so clicky doesn't humanize it (a numeric
// cell value like 33300 would otherwise render as "33.3K").
func psPID(r PSRow) string {
	if r.Live != nil && r.Live.PID > 0 {
		return strconv.Itoa(r.Live.PID)
	}
	return ""
}

func intOrBlank(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// psSessionID prefers the live-resolved session id, falling back to the record
// id (blank for the "pid:<n>" synthetic id of a process with no transcript).
func psSessionID(r PSRow) string {
	if r.Live != nil && r.Live.SessionID != "" {
		return r.Live.SessionID
	}
	if strings.HasPrefix(r.ID, "pid:") {
		return ""
	}
	return r.ID
}

func psAgentIDs(r PSRow) []string {
	if r.Live == nil {
		return nil
	}
	return r.Live.AgentIDs
}

func psAgentCount(r PSRow) int {
	return len(psAgentIDs(r))
}
