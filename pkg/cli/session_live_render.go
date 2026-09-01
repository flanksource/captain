package cli

import (
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
)

type sessionLiveRow struct {
	SessionRecord
}

// sessionLiveConfiguration is the runtime a live session is running on. It
// carries both halves: a mode alone does not say which family is running it, and
// the single composite id that used to stand in for the pair is gone.
type sessionLiveConfiguration struct {
	provider string
	mode     string
	model    string
	effort   string
}

func (c sessionLiveConfiguration) runtime() string {
	if c.provider == "" || c.mode == "" {
		return sessionLiveConfigValue(strings.TrimSpace(c.provider + " " + c.mode))
	}
	return c.provider + " " + c.mode
}

func (c sessionLiveConfiguration) Pretty() api.Text {
	return api.Text{}.
		Append(c.runtime(), "text-cyan-600").
		Append(" · ", "text-gray-400").
		Append(sessionLiveConfigValue(c.model), "text-purple-600 font-medium").
		Append(" · ", "text-gray-400").
		Append(sessionLiveConfigValue(c.effort), "text-amber-600")
}

func (c sessionLiveConfiguration) String() string   { return c.Pretty().String() }
func (c sessionLiveConfiguration) ANSI() string     { return c.Pretty().ANSI() }
func (c sessionLiveConfiguration) HTML() string     { return c.Pretty().HTML() }
func (c sessionLiveConfiguration) Markdown() string { return c.String() }

func (r SessionLiveResult) Pretty() api.Text {
	diagnostics := map[string]any{
		"Database":         r.Database.Source,
		"DSN":              r.Database.DSN,
		"Read":             api.Human(r.Database.ReadAt, "text-muted"),
		"Latest sample":    sessionLiveTime(r.Database.LatestSampledAt),
		"Latest heartbeat": sessionLiveTime(r.Database.LatestHeartbeatAt),
		"Lease expiry":     sessionLiveTime(r.Database.EarliestLeaseExpiry),
		"Live sessions":    r.Total,
	}
	rows := make([]sessionLiveRow, len(r.Sessions))
	for i := range r.Sessions {
		rows[i] = sessionLiveRow{SessionRecord: r.Sessions[i]}
	}
	return api.Text{}.
		Add(clicky.Map(diagnostics)).
		NewLine().
		Add(api.NewTableFrom(rows))
}

func (sessionLiveRow) Columns() []api.ColumnDef {
	return []api.ColumnDef{
		api.Column("configuration").Label("Agent").MaxWidth(32).Build(),
		api.Column("project").Label("Project").MaxWidth(16).Build(),
		api.Column("session").Label("Session").MaxWidth(sessionIDDisplayWidth).Build(),
		api.Column("pid").Label("PID").MaxWidth(20).Build(),
		api.Column("status").Label("Status").MaxWidth(8).Build(),
		api.Column("title").Label("Title").Build(),
	}
}

func (r sessionLiveRow) Row() map[string]any {
	title := strings.TrimSpace(r.Title)
	if prompt := strings.TrimSpace(r.InitialPrompt); prompt != "" && (r.Source == "codex" || title == "") {
		title, _, _ = strings.Cut(prompt, "\n")
		title = strings.TrimSpace(title)
	}
	row := map[string]any{
		"configuration": sessionLiveConfiguration{provider: r.Provider, mode: r.ModelMode, model: r.Model, effort: r.ReasoningEffort},
		"project":       sessionProjectName(r.SessionRecord),
		"session":       sessionListID(r.ID),
		"title":         title,
	}
	if r.Live == nil {
		return row
	}
	identity := make([]string, 0, 2)
	if r.Live.PID > 0 {
		identity = append(identity, strconv.Itoa(r.Live.PID))
	}
	if r.Live.Surface != nil && r.Live.Surface.SurfaceRef != "" {
		identity = append(identity, r.Live.Surface.SurfaceRef)
	}
	if len(identity) > 0 {
		row["pid"] = strings.Join(identity, " ")
	}
	row["status"] = r.Live.Status
	return row
}

func sessionLiveConfigValue(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return "—"
}

func sessionLiveTime(value *time.Time) api.Textable {
	if value == nil {
		return api.Text{}.Append("—", "text-muted")
	}
	return api.Human(*value, "text-muted")
}
