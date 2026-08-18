package monitor

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fixtureMailbox = "mailboxes/" +
	"a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90.git"

// gitAgentFixture stands up an isolated config naming one git-agent backend,
// plus its mailbox tree, and returns the mailbox path tasks are written into.
func gitAgentFixture(t *testing.T) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), ".captain.yaml")
	captainconfig.SetPathForTesting(configPath)
	t.Cleanup(func() { captainconfig.SetPathForTesting("") })

	servedRoot := t.TempDir()
	require.NoError(t, captainconfig.Update(func(cfg *captainconfig.Config) error {
		cfg.Sandbox.Backends = map[string]captainconfig.SandboxBackend{
			"prod-pool": {Kind: "git-agent", Options: map[string]any{"mailboxRoot": servedRoot}},
		}
		return nil
	}))

	mailbox := filepath.Join(servedRoot, fixtureMailbox)
	require.NoError(t, os.MkdirAll(mailbox, 0o755))
	return mailbox
}

func TestGitAgentIngestRecordsDispatchAndVerdicts(t *testing.T) {
	db := openMonitorTestDB(t)
	monitor, err := New(Config{DB: db, HostID: "test"})
	require.NoError(t, err)
	mailbox := gitAgentFixture(t)

	require.NoError(t, gitagent.SaveTaskState(mailbox, &gitagent.TaskState{
		Task: "task-1", Agent: "worker-01", Base: "main", DispatchCommit: "deadbeef",
		Attempts: 1, Relay: "sync", Policy: gitagent.Policy{Paths: []string{"pkg/**"}, MaxAttempts: 3},
		UpdatedAt: time.Now().UTC(),
	}))

	monitor.ingestGitAgentTasks(t.Context())

	tasks, err := db.ListGitAgentTasks(t.Context(), database.ListGitAgentTasksFilter{})
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "task-1", tasks[0].TaskID)
	assert.Equal(t, "prod-pool", tasks[0].Backend)
	assert.Equal(t, "worker-01", tasks[0].Agent)
	assert.Equal(t, fixtureMailbox, tasks[0].Mailbox)
	// One attempt recorded but no verdict yet: the task is still in flight.
	assert.Equal(t, database.GitAgentTaskRunning, tasks[0].Status)

	t.Run("a rejection with attempts left keeps the task open", func(t *testing.T) {
		require.NoError(t, gitagent.SaveVerdict(mailbox, gitagent.TierVerdict{
			V: 1, Task: "task-1", Attempt: 1, Tier: "supervisor", Status: gitagent.StatusRejected,
			Findings: []gitagent.Finding{{Hook: "verify", Kind: "exec", Message: "make lint failed"}},
		}))
		monitor.ingestGitAgentTasks(t.Context())

		detail, ok, err := db.GetGitAgentTask(t.Context(), fixtureMailbox, "task-1")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, database.GitAgentTaskRunning, detail.Task.Status,
			"rejection is not termination while the attempt budget has room")
		require.Len(t, detail.Attempts, 1)
		assert.Equal(t, database.GitAgentVerdictRejected, detail.Attempts[0].Status)
		require.Len(t, detail.Attempts[0].Findings, 1)
		assert.Equal(t, "make lint failed", detail.Attempts[0].Findings[0]["message"])
	})

	t.Run("an accepted verdict concludes the task", func(t *testing.T) {
		require.NoError(t, gitagent.SaveTaskState(mailbox, &gitagent.TaskState{
			Task: "task-1", Agent: "worker-01", Base: "main", DispatchCommit: "deadbeef",
			Attempts: 2, Policy: gitagent.Policy{MaxAttempts: 3}, UpdatedAt: time.Now().UTC(),
		}))
		require.NoError(t, gitagent.SaveVerdict(mailbox, gitagent.TierVerdict{
			V: 1, Task: "task-1", Attempt: 2, Tier: "supervisor", Status: gitagent.StatusAccepted,
			Findings: []gitagent.Finding{{Hook: "integrate", Kind: "commit", Path: "captain/task-1"}},
		}))
		monitor.ingestGitAgentTasks(t.Context())

		detail, ok, err := db.GetGitAgentTask(t.Context(), fixtureMailbox, "task-1")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, database.GitAgentTaskAccepted, detail.Task.Status)
		require.NotNil(t, detail.Task.FinalStatus)
		assert.Equal(t, database.GitAgentVerdictAccepted, *detail.Task.FinalStatus)
		assert.Equal(t, "captain/task-1", detail.Task.IntegratedBranch)
		assert.Equal(t, 2, detail.Task.Attempts)
		require.NotNil(t, detail.Task.ConcludedAt)
	})

	// The pass runs on every backfill tick, so replaying unchanged state must be
	// a no-op rather than duplicating rows or reopening a concluded task.
	t.Run("re-running the pass over unchanged state changes nothing", func(t *testing.T) {
		monitor.ingestGitAgentTasks(t.Context())
		monitor.ingestGitAgentTasks(t.Context())

		tasks, err := db.ListGitAgentTasks(t.Context(), database.ListGitAgentTasksFilter{})
		require.NoError(t, err)
		require.Len(t, tasks, 1)
		assert.Equal(t, database.GitAgentTaskAccepted, tasks[0].Status)

		detail, ok, err := db.GetGitAgentTask(t.Context(), fixtureMailbox, "task-1")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Len(t, detail.Attempts, 2, "verdicts are keyed by attempt, not appended")
	})
}

// The agent host runs the receiver with no database and no configured backend;
// the pass must find nothing to do rather than error.
func TestGitAgentIngestIsANoOpWithoutAMailbox(t *testing.T) {
	db := openMonitorTestDB(t)
	monitor, err := New(Config{DB: db, HostID: "test"})
	require.NoError(t, err)

	configPath := filepath.Join(t.TempDir(), ".captain.yaml")
	captainconfig.SetPathForTesting(configPath)
	t.Cleanup(func() { captainconfig.SetPathForTesting("") })

	monitor.ingestGitAgentTasks(t.Context())

	tasks, err := db.ListGitAgentTasks(t.Context(), database.ListGitAgentTasksFilter{})
	require.NoError(t, err)
	assert.Empty(t, tasks)
}
