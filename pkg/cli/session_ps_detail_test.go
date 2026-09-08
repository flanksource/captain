package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/flanksource/clicky/api"
)

func TestPSContextBarStyleEscalatesAsWindowFills(t *testing.T) {
	for _, tc := range []struct {
		free int
		want string
	}{
		{free: 100, want: "text-green-500"},
		{free: 76, want: "text-green-500"},
		{free: 50, want: "text-green-500"},
		{free: 49, want: "text-amber-500"},
		{free: 35, want: "text-amber-500"},
		{free: 20, want: "text-amber-500"},
		{free: 19, want: "text-red-500"},
		{free: 8, want: "text-red-500"},
		{free: 0, want: "text-red-500"},
	} {
		if got := psContextBarStyle(tc.free); got != tc.want {
			t.Errorf("psContextBarStyle(%d) = %q, want %q", tc.free, got, tc.want)
		}
	}
}

func TestPSContextBarFillsProportionally(t *testing.T) {
	for _, tc := range []struct {
		free int
		want string
	}{
		{free: 76, want: "████████░░"},
		{free: 35, want: "████░░░░░░"},
		{free: 8, want: "█░░░░░░░░░"},
		{free: 100, want: "██████████"},
		{free: 0, want: "░░░░░░░░░░"},
	} {
		bar := psContextBar(&SessionContextWire{
			UsedTokens: 238385, WindowTokens: 1000000, FreePercent: tc.free,
		})
		if bar == nil {
			t.Fatalf("free=%d: context bar is nil", tc.free)
		}
		if got := bar.String(); !strings.Contains(got, tc.want) {
			t.Errorf("free=%d: bar = %q, want it to contain %q", tc.free, got, tc.want)
		}
	}
}

func TestPSContextBarOmittedWithoutAWindow(t *testing.T) {
	if bar := psContextBar(nil); bar != nil {
		t.Fatalf("nil context = %v, want nil", bar)
	}
	// A record can carry a context block with no window size; there is nothing
	// to draw a proportion against.
	if bar := psContextBar(&SessionContextWire{FreePercent: 50}); bar != nil {
		t.Fatalf("windowless context = %v, want nil", bar)
	}
}

func TestPSEnvironmentRowsTruncatesAndCounts(t *testing.T) {
	secret := strings.Repeat("s", 200)
	environment := map[string]string{
		"CLAUDE_CODE_SESSION_ID": "sess-a",
		"CMUX_SOCKET_CAPABILITY": secret,
		"CMUX_SURFACE_ID":        "SURFACE-1",
		"PATH":                   "/usr/bin",
		"HOME":                   "/Users/acme",
		"LANG":                   "en_US.UTF-8",
	}
	rows := psEnvironmentRows(&SessionLiveWire{Environment: environment})
	if len(rows) == 0 {
		t.Fatal("no environment rows")
	}
	rendered := renderPSRows(rows)

	if !strings.Contains(rendered, "CLAUDE_CODE_SESSION_ID=sess-a") {
		t.Errorf("session id not shown:\n%s", rendered)
	}
	// The three uninteresting variables collapse into a count.
	if !strings.Contains(rendered, "(+3 more)") {
		t.Errorf("uninteresting variables not collapsed:\n%s", rendered)
	}
	for _, key := range []string{"PATH", "HOME", "LANG"} {
		if strings.Contains(rendered, key+"=") {
			t.Errorf("uninteresting variable %s was rendered:\n%s", key, rendered)
		}
	}
	// A capability token must never reach the terminal in full.
	if strings.Contains(rendered, secret) {
		t.Errorf("long value was not truncated:\n%s", rendered)
	}
	if !strings.Contains(rendered, "…") {
		t.Errorf("truncation marker missing:\n%s", rendered)
	}
}

func TestPSEnvironmentRowsCountsOnlyUninteresting(t *testing.T) {
	rows := psEnvironmentRows(&SessionLiveWire{Environment: map[string]string{
		"PATH": "/usr/bin", "HOME": "/Users/acme",
	}})
	rendered := renderPSRows(rows)
	if !strings.Contains(rendered, "2 variables") {
		t.Errorf("rendered = %q, want a bare variable count", rendered)
	}
}

func TestPSDetailBlockRendersTheFullInspection(t *testing.T) {
	started := time.Now().Add(-90 * time.Minute)
	row := PSRow{SessionRecord{
		ID: "sess-detail", Source: "claude", Model: "claude-opus-5",
		Project: "captain", GitBranch: "main", CWD: "/Users/acme/src/captain",
		Messages: 318, ToolCalls: 133, CostUSD: 40,
		Tokens: &SessionTokensWire{
			InputTokens: 264, OutputTokens: 73261,
			CacheReadTokens: 21073669, CacheCreationTokens: 210699,
			TotalTokens: 21357893,
		},
		Context: &SessionContextWire{UsedTokens: 238385, WindowTokens: 1000000, FreePercent: 76},
		Live: &SessionLiveWire{
			PID: 22979, PPID: 21271, Status: "sleeping", Active: true,
			CPUPercent: 15.9, RSSBytes: 32210944, StartedAt: &started,
			Command:     "/usr/local/bin/claude --session-id sess-detail",
			SessionID:   "sess-detail",
			SessionFile: "/Users/acme/.claude/projects/p/sess-detail.jsonl",
			Surface:     &CmuxSurface{SurfaceID: "SURFACE-1", SurfaceRef: "surface:130", Workspace: "acme", Port: 9140},
			Environment: map[string]string{"CLAUDE_CODE_SESSION_ID": "sess-detail"},
		},
		Health: []SessionHealthWire{{Kind: "cost_spike", Severity: "warning", Message: "Estimated session cost is above $5"}},
	}}

	got := psDetailBlock(row).String()
	for _, want := range []string{
		"claude", "claude-opus-5", "sleeping", "PID 22979",
		"Session", "sess-detail",
		"Project", "captain (main)",
		"Context", "76% free", "████████░░",
		"Usage", "$40.00",
		"in 264", "out 73K", "cache r 21M / w 210K",
		"Volume", "318 messages", "133 tool calls",
		"Process", "ppid 21271", "cpu 15.9%", "rss 30.7M",
		"up 1.5h", "started ",
		"Cwd", "Command", "Cmux", "surface:130", "port 9140",
		"Transcript", "sess-detail.jsonl",
		"Environment", "CLAUDE_CODE_SESSION_ID=sess-detail",
		"Estimated session cost is above $5",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("detail block missing %q:\n%s", want, got)
		}
	}
}

// TestPSDetailBlockOmitsEmptyRows verifies a non-agent PID degrades to what it
// actually has rather than a column of empty labels.
func TestPSDetailBlockOmitsEmptyRows(t *testing.T) {
	started := time.Now().Add(-2 * time.Hour)
	row := PSRow{SessionRecord{
		ID: "pid:1", Source: "launchd",
		Live: &SessionLiveWire{
			PID: 1, Status: "sleeping", CPUPercent: 0.3, RSSBytes: 8 << 20,
			StartedAt: &started, Command: "/sbin/launchd",
		},
	}}

	got := psDetailBlock(row).String()
	for _, want := range []string{"launchd", "PID 1", "Process", "cpu 0.3%", "Command", "/sbin/launchd"} {
		if !strings.Contains(got, want) {
			t.Errorf("detail block missing %q:\n%s", want, got)
		}
	}
	// Nothing is known about a session, so none of these labels may appear.
	for _, absent := range []string{"Session", "Context", "Usage", "Volume", "Cmux", "Transcript", "Environment", "Project"} {
		if strings.Contains(got, absent) {
			t.Errorf("detail block rendered empty label %q:\n%s", absent, got)
		}
	}
	// The synthetic "pid:<n>" id is not a session identifier worth showing.
	if strings.Contains(got, "pid:1") {
		t.Errorf("synthetic id leaked into the block:\n%s", got)
	}
}

// TestPSResultPrettyRendersTableForScans verifies the listing mode is untouched:
// only a PID inspection switches to detail blocks.
func TestPSResultPrettyRendersTableForScans(t *testing.T) {
	result := PSResult{
		Source: "all", Scope: "current", Total: 1, Live: 1, Active: 1, Tokens: "66M", Cost: "$40.00",
		Sessions: []PSRow{{SessionRecord{
			ID: "sess-a", Source: "claude", CWD: "/Users/acme/src/captain",
			Live: &SessionLiveWire{PID: 4242, Status: "active", SessionID: "sess-a"},
		}}},
	}

	got := result.Pretty().String()
	if !strings.Contains(got, "│") {
		t.Errorf("scan mode did not render a table:\n%s", got)
	}
	for _, want := range []string{"Source", "Scope", "Total", "4242"} {
		if !strings.Contains(got, want) {
			t.Errorf("scan output missing %q:\n%s", want, got)
		}
	}
	// Empty summary fields must not print as bare labels. "Project" also names
	// a table column, so match the summary form specifically.
	if strings.Contains(got, "Project: ") {
		t.Errorf("empty Project was rendered in the summary:\n%s", got)
	}
	// An omitted pair must not leave a blank line behind in the summary.
	summary, _, _ := strings.Cut(got, "╭")
	for i, line := range strings.Split(strings.Trim(summary, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			t.Errorf("summary line %d is blank:\n%s", i, summary)
		}
	}
}

func TestPSResultPrettyIsStableAcrossRuns(t *testing.T) {
	result := PSResult{Source: "all", Scope: "current", Total: 2, Live: 2, Active: 1, Alerts: 1}
	first := result.Pretty().String()
	for i := 0; i < 20; i++ {
		if got := result.Pretty().String(); got != first {
			t.Fatalf("run %d differs from the first render:\n%s\n---\n%s", i, first, got)
		}
	}
}

func TestPSResultPrettyRendersDetailForPIDs(t *testing.T) {
	result := PSResult{
		Source: "all", Scope: psScopePID, Total: 2, Live: 2, Active: 2,
		Sessions: []PSRow{
			{SessionRecord{ID: "pid:1", Source: "launchd", Live: &SessionLiveWire{PID: 1, Command: "/sbin/launchd"}}},
			{SessionRecord{ID: "pid:2", Source: "zsh", Live: &SessionLiveWire{PID: 2, Command: "-zsh"}}},
		},
	}

	got := result.Pretty().String()
	if strings.Contains(got, "│") {
		t.Errorf("PID mode rendered a table:\n%s", got)
	}
	for _, want := range []string{"PID 1", "/sbin/launchd", "PID 2", "-zsh"} {
		if !strings.Contains(got, want) {
			t.Errorf("PID output missing %q:\n%s", want, got)
		}
	}
}

func TestPSShortenPathElidesInteriorDirectories(t *testing.T) {
	long := "/Users/acme/.claude/projects/-Users-acme-go-src-github-com-flanksource-captain/8c630de6-bdfc-41e6-8dd6-b6d999347dbe.jsonl"
	got := psShortenPath(long)
	if len([]rune(got)) > psDetailPathWidth {
		t.Errorf("path %q is %d runes, want <= %d", got, len([]rune(got)), psDetailPathWidth)
	}
	if !strings.HasSuffix(got, "8c630de6-bdfc-41e6-8dd6-b6d999347dbe.jsonl") {
		t.Errorf("path %q lost its basename", got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("path %q was not elided", got)
	}

	short := "/tmp/a.jsonl"
	if got := psShortenPath(short); got != short {
		t.Errorf("short path = %q, want it untouched", got)
	}
	if got := psShortenPath(""); got != "" {
		t.Errorf("empty path = %q, want empty", got)
	}
}

func renderPSRows(rows []api.Text) string {
	parts := make([]string, len(rows))
	for i, row := range rows {
		parts[i] = row.String()
	}
	return strings.Join(parts, "\n")
}
