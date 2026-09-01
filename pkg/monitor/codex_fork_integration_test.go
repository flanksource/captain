package monitor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const codexParentRollout = `{"timestamp":"2026-08-20T09:00:00.000Z","ordinal":0,"type":"session_meta","payload":{"id":"parent-thread","session_id":"parent-thread","cwd":"/repo","thread_source":"user"}}
{"timestamp":"2026-08-20T09:00:00.001Z","ordinal":1,"type":"turn_context","payload":{"turn_id":"parent-turn","model":"gpt-5","effort":"medium"}}
{"timestamp":"2026-08-20T09:00:00.002Z","ordinal":2,"type":"event_msg","payload":{"type":"task_started","turn_id":"parent-turn"}}
{"timestamp":"2026-08-20T09:00:01.000Z","ordinal":3,"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"parent answered"}]}}
{"timestamp":"2026-08-20T09:00:02.000Z","ordinal":4,"type":"event_msg","payload":{"type":"task_complete","turn_id":"parent-turn"}}
`

// codexChildRollout replays the parent's meta and turn (ordinals 1-4) before
// its own history starts at ordinal 5, as Codex writes for a subagent thread.
const codexChildRollout = `{"timestamp":"2026-08-20T09:29:32.898Z","ordinal":0,"type":"session_meta","payload":{"id":"child-thread","session_id":"parent-thread","forked_from_id":"parent-thread","parent_thread_id":"parent-thread","cwd":"/repo","thread_source":"subagent","agent_nickname":"Descartes","agent_path":"/root/contract","subagent_history_start_ordinal":5}}
{"timestamp":"2026-08-20T09:00:00.000Z","ordinal":1,"type":"session_meta","payload":{"id":"parent-thread","session_id":"parent-thread","cwd":"/repo","thread_source":"user"}}
{"timestamp":"2026-08-20T09:00:00.001Z","ordinal":2,"type":"turn_context","payload":{"turn_id":"parent-turn","model":"gpt-5","effort":"medium"}}
{"timestamp":"2026-08-20T09:00:00.002Z","ordinal":3,"type":"event_msg","payload":{"type":"task_started","turn_id":"parent-turn"}}
{"timestamp":"2026-08-20T09:00:01.000Z","ordinal":4,"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"parent answered"}]}}
{"timestamp":"2026-08-20T09:29:35.000Z","ordinal":5,"type":"event_msg","payload":{"type":"task_started","turn_id":"child-turn"}}
{"timestamp":"2026-08-20T09:29:35.001Z","ordinal":6,"type":"turn_context","payload":{"turn_id":"child-turn","model":"gpt-5","effort":"medium"}}
{"timestamp":"2026-08-20T09:29:36.000Z","ordinal":7,"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"child answered"}]}}
{"timestamp":"2026-08-20T09:29:37.000Z","ordinal":8,"type":"event_msg","payload":{"type":"task_complete","turn_id":"child-turn"}}
`

// A subagent thread replays its parent's turn; before the fix both rollouts
// were filed under the parent's thread id and the replayed turn collided with
// the parent's own row on (session_id, provider_turn_id).
func TestRunOnceFilesCodexSubagentUnderItsOwnThread(t *testing.T) {
	db := openMonitorTestDB(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".codex", "sessions", "2026", "08", "20")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rollout-2026-08-20T09-00-00-parent-thread.jsonl"), []byte(codexParentRollout), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rollout-2026-08-20T09-29-32-child-thread.jsonl"), []byte(codexChildRollout), 0o644))

	noProcesses := func() ([]Process, error) { return nil, nil }
	require.NoError(t, RunOnce(t.Context(), Config{DB: db, HostID: "test-host", DiscoverProcesses: noProcesses}))

	var rows []struct {
		ProviderSessionID string
		ParentProvider    *string
		AgentType         *string
		Turns             int
	}
	require.NoError(t, db.Gorm().WithContext(t.Context()).Raw(`
		SELECT s.provider_session_id, p.provider_session_id AS parent_provider, s.agent_type,
		       (SELECT count(*) FROM captain_turns t WHERE t.session_id = s.id) AS turns
		FROM captain_sessions s
		LEFT JOIN captain_sessions p ON p.id = s.parent_session_id
		WHERE s.provider_session_id IN (?, ?)
		ORDER BY s.provider_session_id`, "child-thread", "parent-thread").Scan(&rows).Error)
	require.Len(t, rows, 2, "each thread is its own session: %+v", rows)

	child, parent := rows[0], rows[1]
	assert.Equal(t, "child-thread", child.ProviderSessionID)
	require.NotNil(t, child.ParentProvider)
	assert.Equal(t, "parent-thread", *child.ParentProvider)
	require.NotNil(t, child.AgentType)
	assert.Equal(t, "subagent", *child.AgentType)
	assert.Equal(t, 1, child.Turns, "only the child's own turn, not the replayed parent turn")
	assert.Equal(t, "parent-thread", parent.ProviderSessionID)
	assert.Nil(t, parent.ParentProvider)
	assert.Equal(t, 1, parent.Turns)
}
