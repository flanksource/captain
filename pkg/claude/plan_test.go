package claude

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func toolUseEntry(name string, input map[string]any) HistoryEntry {
	raw, _ := json.Marshal(input)
	return HistoryEntry{
		Message: Message{
			Role:    MessageRoleAssistant,
			Content: []ContentBlock{{Type: ContentTypeToolUse, Name: name, Input: raw}},
		},
	}
}

func TestPlanFromEntries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	plansDir := filepath.Join(home, ".claude", "plans")
	plan := func(slug string) string { return filepath.Join(plansDir, slug+".md") }

	tests := []struct {
		name         string
		entries      []HistoryEntry
		wantNil      bool
		wantPath     string
		wantContent  string
		wantExplicit bool
		wantSlug     string
	}{
		{
			name: "exit plan mode carries path and content",
			entries: []HistoryEntry{
				{Slug: "tidy-otter"},
				toolUseEntry("ExitPlanMode", map[string]any{
					"planFilePath": plan("tidy-otter"),
					"plan":         "# Plan\n\nstep one",
				}),
			},
			wantPath:     plan("tidy-otter"),
			wantContent:  "# Plan\n\nstep one",
			wantExplicit: true,
			wantSlug:     "tidy-otter",
		},
		{
			name: "write to plans dir captures full content",
			entries: []HistoryEntry{
				toolUseEntry("Write", map[string]any{
					"file_path": plan("brave-fox"),
					"content":   "# Brave fox plan",
				}),
			},
			wantPath:     plan("brave-fox"),
			wantContent:  "# Brave fox plan",
			wantExplicit: true,
		},
		{
			name: "edit to plans dir sets path but not fragment content",
			entries: []HistoryEntry{
				toolUseEntry("Edit", map[string]any{
					"file_path":  plan("calm-lynx"),
					"new_string": "patched line",
				}),
			},
			wantPath:     plan("calm-lynx"),
			wantContent:  "",
			wantExplicit: true,
		},
		{
			name:         "slug only derives path but is not explicit",
			entries:      []HistoryEntry{{Slug: "quiet-heron"}},
			wantPath:     plan("quiet-heron"),
			wantExplicit: false,
			wantSlug:     "quiet-heron",
		},
		{
			name: "plan-mode attachment is explicit",
			entries: []HistoryEntry{
				{Slug: "swift-marten", PlanFilePath: plan("swift-marten")},
			},
			wantPath:     plan("swift-marten"),
			wantExplicit: true,
			wantSlug:     "swift-marten",
		},
		{
			name: "later exit plan mode supersedes earlier inline content",
			entries: []HistoryEntry{
				toolUseEntry("ExitPlanMode", map[string]any{"planFilePath": plan("revised"), "plan": "v1"}),
				toolUseEntry("ExitPlanMode", map[string]any{"planFilePath": plan("revised"), "plan": "v2 final"}),
			},
			wantPath:     plan("revised"),
			wantContent:  "v2 final",
			wantExplicit: true,
		},
		{
			name: "write outside plans dir is not a plan",
			entries: []HistoryEntry{
				toolUseEntry("Write", map[string]any{"file_path": "/repo/main.go", "content": "x"}),
			},
			wantNil: true,
		},
		{
			name:    "no signals returns nil",
			entries: []HistoryEntry{{Message: Message{Role: MessageRoleUser}}},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PlanFromEntries(tt.entries)
			if tt.wantNil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tt.wantPath, got.Path)
			assert.Equal(t, tt.wantContent, got.Content)
			assert.Equal(t, tt.wantExplicit, got.Explicit)
			if tt.wantSlug != "" {
				assert.Equal(t, tt.wantSlug, got.Slug)
			}
		})
	}
}
