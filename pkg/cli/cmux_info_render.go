package cli

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/flanksource/clicky/api"
)

func (r CmuxInfoResult) Pretty() api.Text {
	return api.Text{}.Add(api.NewTableFrom(r.Processes))
}

func (r CmuxProcess) Columns() []api.ColumnDef {
	return []api.ColumnDef{
		api.Column("pid").Label("PID").Build(),
		api.Column("ppid").Label("PPID").Build(),
		api.Column("runtime").Label("Runtime").Build(),
		api.Column("process").Label("Process").MaxWidth(24).Build(),
		api.Column("cpu").Label("CPU").Build(),
		api.Column("rss").Label("RSS").Build(),
		api.Column("listeners").Label("Listeners").MaxWidth(30).Build(),
		api.Column("surface").Label("Surface").MaxWidth(24).Build(),
	}
}

func (r CmuxProcess) Row() map[string]any {
	row := map[string]any{
		"pid":     strconv.Itoa(r.PID),
		"runtime": r.Runtime,
		"process": r.Name,
		"cpu":     fmt.Sprintf("%.1f%%", r.CPUPercent),
		"rss":     api.HumanizeBytes(int64(r.RSSBytes)),
	}
	if r.PPID > 0 {
		row["ppid"] = strconv.Itoa(r.PPID)
	}
	if listeners := r.listenerLabels(); len(listeners) > 0 {
		row["listeners"] = api.CompactList(listeners)
	}
	if surfaces := r.surfaceLabels(); len(surfaces) > 0 {
		row["surface"] = api.CompactList(surfaces)
	}
	return row
}

func (r CmuxProcess) RowDetail() api.Textable {
	items := []api.KeyValuePair{
		api.KeyValue("Executable", r.Executable),
		api.KeyValue("Command", r.Command),
		api.KeyValue("Inspection error", r.InspectionError),
		api.KeyValue("Stack error", r.StackError),
	}
	detail := api.Text{}.Add(api.DescriptionList{Items: items})
	if locations := r.locationLabels(); len(locations) > 0 {
		detail = detail.NewLine().Append("Locations: ", "text-muted").Add(api.CompactList(locations))
	}
	if r.Stack != "" {
		detail = detail.NewLine().Add(api.CodeBlock("text/plain", r.Stack))
	}
	return detail
}

func (l CmuxListener) String() string {
	host := l.Address
	if host == "" {
		host = "*"
	}
	return l.Protocol + " " + net.JoinHostPort(host, strconv.FormatUint(uint64(l.Port), 10))
}

func (r CmuxProcess) listenerLabels() []string {
	labels := make([]string, 0, len(r.Listeners))
	for _, listener := range r.Listeners {
		labels = append(labels, listener.String())
	}
	return labels
}

func (r CmuxProcess) surfaceLabels() []string {
	labels := make([]string, 0, len(r.Locations))
	for _, location := range r.Locations {
		label := location.SurfaceRef
		if label == "" {
			label = location.SurfaceID
		}
		if location.SurfaceTitle != "" {
			label = strings.TrimSpace(label + " " + location.SurfaceTitle)
		}
		labels = append(labels, label)
	}
	return labels
}

func (r CmuxProcess) locationLabels() []string {
	labels := make([]string, 0, len(r.Locations))
	for _, location := range r.Locations {
		labels = append(labels, fmt.Sprintf(
			"%s (%s) / %s (%s) / %s (%s) %s",
			location.WorkspaceRef, location.WorkspaceID,
			location.PaneRef, location.PaneID,
			location.SurfaceRef, location.SurfaceID,
			location.TTY,
		))
	}
	return labels
}
