package cli

import (
	"testing"

	"github.com/flanksource/captain/pkg/claude/tools"
)

func titleTool(sessionID, title string) tools.Tool {
	return tools.NewTool(tools.BaseTool{
		RawTool:   "SessionTitle",
		Source:    "claude",
		SessionID: sessionID,
		Input:     map[string]any{"aiTitle": title},
	})
}

func bashTool(sessionID, command string) tools.Tool {
	return tools.NewTool(tools.BaseTool{
		RawTool:   "Bash",
		Source:    "claude",
		SessionID: sessionID,
		Input:     map[string]any{"command": command},
	})
}

func collapsedTitles(t *testing.T, tl []tools.Tool) []string {
	t.Helper()
	var titles []string
	for _, tool := range collapseRepeatedTitles(tl) {
		if title, ok := tool.(*tools.SessionTitleTool); ok {
			titles = append(titles, title.Str("aiTitle"))
		}
	}
	return titles
}

// TestCollapseRepeatedTitles_ObservedRunPattern reproduces the shape of session
// 929f3d1b: 82 ai-title records covering only 3 distinct titles, each rewritten
// once per turn and therefore interleaved with real tool calls. Only the first
// row of each run should survive.
func TestCollapseRepeatedTitles_ObservedRunPattern(t *testing.T) {
	const sessionID = "929f3d1b"
	runs := []struct {
		title string
		count int
	}{
		{"Update GitHub scraper for OAuth and security settings", 23},
		{"github-scraper-org-settings", 57},
		{"pr-coderabbit-review", 2},
	}

	var tl []tools.Tool
	for _, run := range runs {
		for i := 0; i < run.count; i++ {
			tl = append(tl, titleTool(sessionID, run.title), bashTool(sessionID, "go test ./..."))
		}
	}

	got := collapsedTitles(t, tl)
	want := []string{runs[0].title, runs[1].title, runs[2].title}
	if len(got) != len(want) {
		t.Fatalf("collapsed to %d title rows (%v), want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("title[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	if kept := len(collapseRepeatedTitles(tl)); kept != len(want)+82 {
		t.Errorf("non-title rows must pass through untouched: kept %d rows, want %d", kept, len(want)+82)
	}
}

// TestCollapseRepeatedTitles_ChangesStillRender covers the cases the collapse
// must not swallow: a title reverting to an earlier value, and the same title
// used by a different session.
func TestCollapseRepeatedTitles_ChangesStillRender(t *testing.T) {
	tests := []struct {
		name string
		in   []tools.Tool
		want []string
	}{
		{
			name: "revert to an earlier title gets its own row",
			in: []tools.Tool{
				titleTool("S1", "first"),
				titleTool("S1", "second"),
				titleTool("S1", "second"),
				titleTool("S1", "first"),
			},
			want: []string{"first", "second", "first"},
		},
		{
			name: "identical titles in different sessions both render",
			in: []tools.Tool{
				titleTool("S1", "shared"),
				titleTool("S1", "shared"),
				titleTool("S2", "shared"),
				titleTool("S2", "shared"),
			},
			want: []string{"shared", "shared"},
		},
		{
			name: "no titles at all is a passthrough",
			in:   []tools.Tool{bashTool("S1", "ls")},
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := collapsedTitles(t, tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d titles (%v), want %d (%v)", len(got), got, len(tc.want), tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("title[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
