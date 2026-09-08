package cli

import (
	"fmt"
	"strconv"
	"time"

	"github.com/flanksource/captain/pkg/agentbrowser"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
)

var _ api.TableProvider = BrowserSession{}

func (b BrowserSession) Columns() []api.ColumnDef {
	return []api.ColumnDef{
		api.Column("status").Label("").Build(),
		api.Column("session").Label("Session").MaxWidth(28).Build(),
		api.Column("age").Label("Age").Build(),
		api.Column("cpu").Label("CPU").Build(),
		api.Column("memory").Label("Memory").Build(),
		api.Column("pid").Label("PID").Build(),
		api.Column("agent").Label("Agent Session").MaxWidth(30).Build(),
		api.Column("project").Label("Project").MaxWidth(20).Build(),
	}
}

func (b BrowserSession) Row() map[string]any {
	row := map[string]any{
		"status":  browserStatusIcon(b),
		"session": browserSessionName(b),
		"pid":     intOrBlank(b.DaemonPID),
		"agent":   browserAgentCell(b),
		"project": b.Project,
	}
	if b.StartedAt != nil {
		row["age"] = api.Human(time.Since(*b.StartedAt), "text-muted")
	}
	if b.CPUPercent > 0 {
		row["cpu"] = fmt.Sprintf("%.1f%%", b.CPUPercent)
	}
	if b.MemoryRSSKB > 0 {
		row["memory"] = agentbrowser.FormatRSS(b.MemoryRSSKB)
	}
	return row
}

// RowDetail expands what does not belong in a scannable table: the daemon's own
// sidecar facts, how the agent session was attributed, and the process
// breakdown behind the aggregate CPU/memory figures.
func (b BrowserSession) RowDetail() api.Textable {
	items := []api.KeyValuePair{
		api.KeyValue("Namespace", b.Namespace),
		api.KeyValue("Engine", b.Engine),
		api.KeyValue("Version", b.Version),
		api.KeyValue("Provider", b.Provider),
		api.KeyValue("Stream port", intOrBlank(b.StreamPort)),
		api.KeyValue("Socket", b.SocketPath),
		api.KeyValue("Command", compactSessionCommand(b.Command)),
		api.KeyValue("CWD", b.Link.CWD),
		api.KeyValue("Captain session", b.CaptainSessionID),
		api.KeyValue("Link source", string(b.Link.Source)),
	}
	if surface := b.Link.Surface; surface != nil {
		items = append(items,
			api.KeyValue("Workspace", surface.Workspace),
			api.KeyValue("Surface", surface.SurfaceID),
		)
	}
	text := api.Text{}.Add(api.DescriptionList{Items: items})
	for _, proc := range b.Processes {
		text = text.NewLine().
			Append(strconv.Itoa(proc.PID), "text-muted").Space().
			Append(fmt.Sprintf("%.1f%% cpu", proc.CPUPercent), "text-muted").Space().
			Append(agentbrowser.FormatRSS(proc.MemoryRSSKB), "text-muted").Space().
			Append(compactSessionCommand(proc.Command), "")
	}
	return text
}

// browserStatusIcon distinguishes a running daemon from sidecars left behind by
// one that died.
func browserStatusIcon(b BrowserSession) api.Textable {
	if b.State != browserStateLive {
		return icons.Info
	}
	if b.Link.Source == BrowserLinkNone {
		return icons.Warning
	}
	return icons.Success
}

// browserSessionName qualifies the name with its namespace, because the same
// session name exists independently in every namespace.
func browserSessionName(b BrowserSession) string {
	if b.Namespace == "" {
		return b.Session
	}
	return b.Namespace + "/" + b.Session
}

// browserAgentCell merges the owning agent's source and session id into one
// cell: "claude 8c630de6". An unattributed browser renders blank rather than
// borrowing an identity it could not prove.
func browserAgentCell(b BrowserSession) api.Text {
	if b.Link.AgentSessionID == "" {
		return api.Text{}
	}
	text := psSourceText(b.Link.AgentSource)
	return text.Space().Append(shortSessionID(b.Link.AgentSessionID), "text-muted")
}
