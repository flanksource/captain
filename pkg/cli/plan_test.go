package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// claudePlanSession writes a Claude session whose assistant turn exits plan mode
// against planPath, carrying inlineBody as the inline plan copy.
func claudePlanSession(t *testing.T, home, project, id, slug, planPath, inlineBody string) {
	t.Helper()
	sessionFile := filepath.Join(home, ".claude", "projects", claude.NormalizePath(project), id+".jsonl")
	writeJSONL(t, sessionFile,
		map[string]any{
			"type": "user", "sessionId": id, "uuid": "u1",
			"timestamp": "2026-06-01T10:00:00Z", "cwd": project, "slug": slug,
			"message": map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "make a plan"},
			}},
		},
		map[string]any{
			"type": "assistant", "sessionId": id, "uuid": "a1",
			"timestamp": "2026-06-01T10:00:01Z", "cwd": project, "slug": slug,
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "tu1", "name": "ExitPlanMode",
					"input": map[string]any{"planFilePath": planPath, "plan": inlineBody}},
			}},
		},
	)
}

func TestRunPlanClaudePrefersDiskContent(t *testing.T) {
	home := t.TempDir()
	withTestCaptainDB(t)
	t.Setenv("HOME", home)
	project := filepath.Join(home, "work", "proj")
	require.NoError(t, os.MkdirAll(project, 0o755))
	t.Chdir(project)

	planPath := filepath.Join(home, ".claude", "plans", "tidy-otter.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(planPath), 0o755))
	require.NoError(t, os.WriteFile(planPath, []byte("# Tidy otter\n\non-disk body"), 0o644))
	claudePlanSession(t, home, project, "sess-plan", "tidy-otter", planPath, "# inline body")

	got, err := RunPlan(PlanOptions{SessionID: "sess-plan", Source: "claude"})
	require.NoError(t, err)
	assert.Equal(t, "sess-plan", got.SessionID)
	assert.Equal(t, "claude", got.Source)
	assert.Equal(t, planPath, got.Path)
	assert.True(t, got.OnDisk)
	assert.Equal(t, "tidy-otter", got.Slug)
	assert.Equal(t, "# Tidy otter\n\non-disk body", got.Content)
}

func TestRunPlanClaudeDefaultSourceUsesDirectSessionLookup(t *testing.T) {
	home := t.TempDir()
	withTestCaptainDB(t)
	t.Setenv("HOME", home)
	project := filepath.Join(home, "work", "proj")
	require.NoError(t, os.MkdirAll(project, 0o755))
	t.Chdir(project)

	planPath := filepath.Join(home, ".claude", "plans", "quick-plan.md")
	claudePlanSession(t, home, project, "sess-direct", "quick-plan", planPath, "# direct lookup")

	got, err := RunPlan(PlanOptions{SessionID: "sess-direct"})
	require.NoError(t, err)
	assert.Equal(t, "sess-direct", got.SessionID)
	assert.Equal(t, "claude", got.Source)
	assert.Equal(t, "# direct lookup", got.Content)
}

func TestRunPlanClaudeInlineWhenMissingOnDisk(t *testing.T) {
	home := t.TempDir()
	withTestCaptainDB(t)
	t.Setenv("HOME", home)
	project := filepath.Join(home, "work", "proj")
	require.NoError(t, os.MkdirAll(project, 0o755))
	t.Chdir(project)

	planPath := filepath.Join(home, ".claude", "plans", "lone-wolf.md")
	claudePlanSession(t, home, project, "sess-inline", "lone-wolf", planPath, "# inline only")

	got, err := RunPlan(PlanOptions{SessionID: "sess-inline", Source: "claude"})
	require.NoError(t, err)
	assert.False(t, got.OnDisk)
	assert.Equal(t, planPath, got.Path)
	assert.Equal(t, "# inline only", got.Content)
}

func TestRunPlanClaudeNoPlanErrors(t *testing.T) {
	home := t.TempDir()
	withTestCaptainDB(t)
	t.Setenv("HOME", home)
	project := filepath.Join(home, "work", "proj")
	require.NoError(t, os.MkdirAll(project, 0o755))
	t.Chdir(project)

	// A session with a slug but no plan signal and no plan file is not a plan.
	sessionFile := filepath.Join(home, ".claude", "projects", claude.NormalizePath(project), "sess-noplan.jsonl")
	writeJSONL(t, sessionFile,
		map[string]any{
			"type": "assistant", "sessionId": "sess-noplan", "uuid": "a1",
			"timestamp": "2026-06-01T10:00:01Z", "cwd": project, "slug": "plain-otter",
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "tu1", "name": "Read",
					"input": map[string]any{"file_path": "README.md"}},
			}},
		},
	)

	_, err := RunPlan(PlanOptions{SessionID: "sess-noplan", Source: "claude"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no plan")
}

func TestRunPlanCodexChecklist(t *testing.T) {
	home := t.TempDir()
	withTestCaptainDB(t)
	t.Setenv("HOME", home)
	project := filepath.Join(home, "work", "proj")
	require.NoError(t, os.MkdirAll(project, 0o755))
	t.Chdir(project)

	codexFile := filepath.Join(home, ".codex", "sessions", "2026", "06", "rollout-plan.jsonl")
	writeJSONL(t, codexFile,
		map[string]any{
			"timestamp": "2026-06-01T10:00:00Z", "type": "session_meta",
			"payload": map[string]any{"id": "codex-plan", "cwd": project, "cli_version": "0.1.0"},
		},
		map[string]any{
			"timestamp": "2026-06-01T10:00:01Z", "type": "response_item",
			"payload": map[string]any{
				"type": "function_call", "name": "update_plan", "call_id": "c1",
				"arguments": `{"plan":[{"step":"first thing","status":"completed"},{"step":"second thing","status":"in_progress"}]}`,
			},
		},
	)

	got, err := RunPlan(PlanOptions{SessionID: "codex-plan", Source: "codex"})
	require.NoError(t, err)
	assert.Equal(t, "codex", got.Source)
	assert.Empty(t, got.Path)
	assert.Equal(t, "- [x] first thing\n- [ ] second thing _(in progress)_", got.Content)
}
