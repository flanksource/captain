package cli

import (
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai/history"
)

func TestCodexSessionMatchesProject(t *testing.T) {
	root := "/Users/me/project"
	tests := []struct {
		name string
		uses []history.ToolUse
		root string
		want bool
	}{
		{
			name: "empty root matches anything",
			uses: []history.ToolUse{{CWD: "/anywhere"}},
			root: "",
			want: true,
		},
		{
			name: "exact cwd match",
			uses: []history.ToolUse{{CWD: root}},
			root: root,
			want: true,
		},
		{
			name: "subdirectory of project matches",
			uses: []history.ToolUse{{CWD: root + "/pkg/foo"}},
			root: root,
			want: true,
		},
		{
			name: "sibling project does not match",
			uses: []history.ToolUse{{CWD: "/Users/me/other"}},
			root: root,
			want: false,
		},
		{
			name: "parent of project does not match",
			uses: []history.ToolUse{{CWD: "/Users/me"}},
			root: root,
			want: false,
		},
		{
			name: "no cwd information cannot match",
			uses: []history.ToolUse{{CWD: ""}},
			root: root,
			want: false,
		},
		{
			name: "any matching tool use is enough",
			uses: []history.ToolUse{
				{CWD: "/somewhere/else"},
				{CWD: root + "/cmd"},
			},
			root: root,
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := codexSessionMatchesProject(tt.uses, tt.root)
			if got != tt.want {
				t.Errorf("codexSessionMatchesProject(%q) = %v, want %v", tt.root, got, tt.want)
			}
		})
	}
}

func TestSortSessionsRecent(t *testing.T) {
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	middle := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	sessions := []SessionInfo{
		{ID: "a", StartedAt: &older},
		{ID: "b", StartedAt: nil},
		{ID: "c", StartedAt: &newer},
		{ID: "d", StartedAt: &middle},
	}
	sortSessionsRecent(sessions)
	wantOrder := []string{"c", "d", "a", "b"}
	for i, w := range wantOrder {
		if sessions[i].ID != w {
			t.Errorf("position %d = %q, want %q (full=%v)", i, sessions[i].ID, w, sessions)
		}
	}
}

func TestShortID(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"6522fe00-9a7c-4cee-a84d-39efca51d798", "6522fe00"},
		{"short", "short"},
		{"", ""},
		{"019e03c1-1234", "019e03c1"},
	}
	for _, tc := range tests {
		if got := shortID(tc.in); got != tc.want {
			t.Errorf("shortID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSessionInfoPretty(t *testing.T) {
	ts := time.Date(2026, 5, 7, 11, 10, 0, 0, time.UTC)
	full := SessionInfo{
		ID:        "abcd1234-rest",
		StartedAt: &ts,
		Model:     "claude-opus-4-7",
		Version:   "2.1.132",
		GitBranch: "feat/x",
		ToolCalls: 42,
	}
	if got := full.Pretty().String(); got == "" {
		t.Fatal("non-empty SessionInfo should render some text")
	}

	empty := SessionInfo{}
	if got := empty.Pretty().String(); got == "" {
		t.Fatal("empty SessionInfo should still render a placeholder")
	}
}

func TestUpdateRange(t *testing.T) {
	stats := SourceStats{}
	t1 := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 4, 9, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 1, 6, 11, 0, 0, 0, time.UTC)

	updateRange(&stats, t1)
	if stats.HistoryStart == nil || !stats.HistoryStart.Equal(t1) {
		t.Fatalf("first update should set start to %v, got %v", t1, stats.HistoryStart)
	}
	if stats.HistoryEnd == nil || !stats.HistoryEnd.Equal(t1) {
		t.Fatalf("first update should set end to %v, got %v", t1, stats.HistoryEnd)
	}

	updateRange(&stats, t2)
	if !stats.HistoryStart.Equal(t2) {
		t.Errorf("earlier timestamp should move start to %v, got %v", t2, *stats.HistoryStart)
	}
	if !stats.HistoryEnd.Equal(t1) {
		t.Errorf("earlier timestamp should not move end, got %v", *stats.HistoryEnd)
	}

	updateRange(&stats, t3)
	if !stats.HistoryStart.Equal(t2) {
		t.Errorf("later timestamp should not move start, got %v", *stats.HistoryStart)
	}
	if !stats.HistoryEnd.Equal(t3) {
		t.Errorf("later timestamp should move end to %v, got %v", t3, *stats.HistoryEnd)
	}
}
