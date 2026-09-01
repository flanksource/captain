package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/cmux"
	"github.com/flanksource/captain/pkg/database"
	clickyapi "github.com/flanksource/clicky/api"
)

func TestSessionLiveResultPrettyReportsDatabaseDiagnostics(t *testing.T) {
	sampledAt := time.Date(2026, time.July, 13, 15, 0, 0, 0, time.UTC)
	expiresAt := sampledAt.Add(time.Minute)
	result := SessionLiveResult{
		Sessions: []SessionRecord{{
			ID: "6522fe00-9a7c-4cee-a205-123456789abc", Source: "codex", Project: "/work/captain",
			Provider: "openai", ModelMode: "cmux", Model: "gpt-5.6-sol", ReasoningEffort: "high",
			InitialPrompt: "Diagnose the monitor lock without polling",
			Live: &SessionLiveWire{
				PID: 24680, Status: "sleeping", SampledAt: &sampledAt, LastHeartbeatAt: &sampledAt,
				LeaseOwner: "captain-serve", LeaseExpiresAt: &expiresAt,
			},
		}},
		Total: 1,
		Database: SessionDatabaseStatusWire{
			Source: "captain embedded database", DSN: "postgres://localhost/captain", ReadAt: sampledAt,
			LatestSampledAt: &sampledAt, LatestHeartbeatAt: &sampledAt, EarliestLeaseExpiry: &expiresAt,
		},
	}

	rendered := result.Pretty().String()
	for _, expected := range []string{
		"captain embedded database", "Latest sample", "Latest heartbeat", "Lease expiry",
		"6522fe00", "24680", "sleeping",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("pretty output missing %q: %s", expected, rendered)
		}
	}
}

func TestSessionLiveResultPrettyKeepsUnavailableDatabaseDiagnosticsVisible(t *testing.T) {
	rendered := (SessionLiveResult{Database: SessionDatabaseStatusWire{ReadAt: time.Now()}}).Pretty().String()
	for _, expected := range []string{"Latest sample", "Latest heartbeat", "Lease expiry", "—"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("pretty output missing %q: %s", expected, rendered)
		}
	}
}

func TestSessionLiveRowColumnsIncludeModelConfigurationAndTitle(t *testing.T) {
	row := sessionLiveRow{SessionRecord: SessionRecord{
		Provider: "openai", ModelMode: "cli", Model: "gpt-5.5", ReasoningEffort: "medium",
		Title: "Stored title", InitialPrompt: "Fallback prompt",
	}}
	wantColumns := []string{
		"configuration", "project", "session", "pid", "status", "title",
	}
	columns := row.Columns()
	if len(columns) != len(wantColumns) {
		t.Fatalf("columns = %+v", columns)
	}
	if columns[0].Label != "Agent" {
		t.Fatalf("configuration label = %q, want Agent", columns[0].Label)
	}
	for i, want := range wantColumns {
		if columns[i].Name != want {
			t.Fatalf("column %d = %q, want %q", i, columns[i].Name, want)
		}
	}
	wantWidths := []int{32, 16, sessionIDDisplayWidth, 20, 8, 0}
	for i, want := range wantWidths {
		if columns[i].MaxWidth != want {
			t.Fatalf("column %q width = %d, want %d", columns[i].Name, columns[i].MaxWidth, want)
		}
	}
	if columns[5].MaxWidth != 0 || columns[5].Style != "" {
		t.Fatalf("title column = %+v", columns[5])
	}
	values := row.Row()
	configuration := values["configuration"].(interface {
		String() string
		HTML() string
		Markdown() string
	})
	if configuration.String() != "openai cli · gpt-5.5 · medium" {
		t.Fatalf("configuration = %q", configuration.String())
	}
	if configuration.Markdown() != configuration.String() {
		t.Fatalf("configuration markdown = %q, want plain text", configuration.Markdown())
	}
	for _, style := range []string{"text-cyan-600", "text-purple-600", "text-amber-600"} {
		if !strings.Contains(configuration.HTML(), style) {
			t.Fatalf("configuration HTML missing %q: %s", style, configuration.HTML())
		}
	}
	ansiTable := clickyapi.NewTableFrom([]sessionLiveRow{row}).ANSI()
	if !strings.Contains(ansiTable, values["configuration"].(interface{ ANSI() string }).ANSI()) {
		t.Fatalf("table ANSI dropped configuration colors: %q", ansiTable)
	}
	if title := values["title"]; title != "Stored title" {
		t.Fatalf("title = %q, want stored title", title)
	}
	withID := sessionLiveRow{SessionRecord: SessionRecord{ID: "6522fe00-9a7c-4cee-a205-123456789abc"}}.Row()
	if withID["session"] != "6522fe00-9a7" {
		t.Fatalf("session = %q, want wider copyable prefix", withID["session"])
	}
}

func TestSessionLiveRowPIDIncludesCmuxSurfaceRef(t *testing.T) {
	row := sessionLiveRow{SessionRecord: SessionRecord{Live: &SessionLiveWire{
		PID: 24680,
		Surface: &CmuxSurface{
			SurfaceRef: "surface:383",
		},
	}}}.Row()
	if got := row["pid"]; got != "24680 surface:383" {
		t.Fatalf("pid = %q, want process and cmux surface refs", got)
	}
}

func TestSessionLiveRowCodexTitleUsesFullFirstPromptLine(t *testing.T) {
	firstLine := "Review dependency installation versions and identify duplication using jscpd"
	row := sessionLiveRow{SessionRecord: SessionRecord{
		Source:        "codex",
		Title:         "Review dependency installation versions and identify duplication…",
		InitialPrompt: firstLine + "\n\nDetails that do not belong in the title",
	}}.Row()
	if got := row["title"]; got != firstLine {
		t.Fatalf("title = %q, want full first prompt line", got)
	}
}

func TestEnrichLiveSessionSurfacesResolvesShortRef(t *testing.T) {
	const (
		pid       = 24680
		surfaceID = "4F846CB1-2EE5-4359-8D5B-A0F6F3837952"
	)
	defer stubPSDiscovery(t, nil, nil, func(gotPID int) *CmuxSurface {
		if gotPID != pid {
			t.Fatalf("surface lookup pid = %d, want %d", gotPID, pid)
		}
		return &CmuxSurface{SurfaceID: surfaceID}
	}, map[string]cmux.Surface{
		surfaceID: {ID: surfaceID, Ref: "surface:383"},
	})()

	records := []SessionRecord{{Live: &SessionLiveWire{PID: pid}}}
	enrichLiveSessionSurfaces(records)
	if got := records[0].Live.Surface; got == nil || got.SurfaceRef != "surface:383" {
		t.Fatalf("surface = %+v, want short cmux ref", got)
	}
}

func TestSessionLiveRowTitleFallsBackToInitialPrompt(t *testing.T) {
	prompt := "Fallback prompt " + strings.Repeat("x", 80)
	liveRow := sessionLiveRow{SessionRecord: SessionRecord{InitialPrompt: prompt}}
	row := liveRow.Row()
	title := row["title"]
	if title != prompt {
		t.Fatalf("title = %q, want full fallback prompt", title)
	}
	if rendered := clickyapi.NewTableFrom([]sessionLiveRow{liveRow}).String(); !strings.Contains(rendered, prompt) {
		t.Fatalf("rendered table truncated title: %s", rendered)
	}
}

func TestSessionLiveRowKeepsEmptyModelConfigurationColumnsVisible(t *testing.T) {
	row := (sessionLiveRow{}).Row()
	configuration := row["configuration"].(interface{ String() string }).String()
	if configuration != "— · — · —" {
		t.Fatalf("configuration = %q, want missing-value markers", configuration)
	}
}

func TestRecordFromOverviewIncludesDatabaseRuntime(t *testing.T) {
	mode := "cmux"
	record := recordFromOverview(database.SessionOverview{Provider: "anthropic", ModelMode: &mode})
	if record.Provider != "anthropic" || record.ModelMode != mode {
		t.Fatalf("runtime = %s %s, want anthropic cmux", record.Provider, record.ModelMode)
	}
}
