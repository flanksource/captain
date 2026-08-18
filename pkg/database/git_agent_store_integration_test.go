package database

import (
	"testing"
	"time"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The ingest watcher re-scans the same mailbox tree on every change and again on
// a startup backfill, so every write has to be replayable: the same scan twice
// must leave one row, and a stale scan must never undo a newer one.
func TestGitAgentTaskStoreIsIdempotentUnderRescan(t *testing.T) {
	handle := dbtest.ForT(t, dbtest.Options{Name: "captain_git_agent_store"})
	db, err := Open(t.Context(), WithDSN(handle.DSN()), WithMigrations())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	dispatchedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	scan := UpsertGitAgentTaskInput{
		TaskID:         "task-1",
		Mailbox:        "mailboxes/aaa.git",
		Repository:     "/repo/project",
		Backend:        "prod-pool",
		Agent:          "worker-01",
		AdmissionKey:   "run-key-1",
		Base:           "main",
		DispatchCommit: "deadbeef",
		Relay:          "sync",
		Policy:         map[string]any{"paths": []string{"pkg/**"}, "maxAttempts": 3},
		Attempts:       1,
		MaxAttempts:    3,
		Status:         GitAgentTaskRunning,
		DispatchedAt:   dispatchedAt,
	}

	id, err := db.UpsertGitAgentTask(t.Context(), scan)
	require.NoError(t, err)

	sameID, err := db.UpsertGitAgentTask(t.Context(), scan)
	require.NoError(t, err)
	assert.Equal(t, id, sameID, "re-scanning must reuse the row, not create a second one")

	tasks, err := db.ListGitAgentTasks(t.Context(), ListGitAgentTasksFilter{})
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "worker-01", tasks[0].Agent)
	assert.Equal(t, map[string]any{"paths": []any{"pkg/**"}, "maxAttempts": float64(3)}, tasks[0].Policy)

	t.Run("a later scan raises the attempt count", func(t *testing.T) {
		next := scan
		next.Attempts = 2
		_, err := db.UpsertGitAgentTask(t.Context(), next)
		require.NoError(t, err)
		detail, ok, err := db.GetGitAgentTask(t.Context(), scan.Mailbox, scan.TaskID)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, 2, detail.Task.Attempts)
	})

	// fsnotify coalesces and the backfill walks the whole tree, so scans can
	// arrive out of order. A stale one must not walk the count backwards.
	t.Run("an out-of-order scan does not lower the attempt count", func(t *testing.T) {
		stale := scan
		stale.Attempts = 1
		_, err := db.UpsertGitAgentTask(t.Context(), stale)
		require.NoError(t, err)
		detail, ok, err := db.GetGitAgentTask(t.Context(), scan.Mailbox, scan.TaskID)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, 2, detail.Task.Attempts, "GREATEST must keep the highest attempt seen")
	})

	// A task directory read mid-write can yield a partial record; it must not
	// blank fields an earlier, more complete scan already established.
	t.Run("a partial scan does not blank known fields", func(t *testing.T) {
		partial := UpsertGitAgentTaskInput{
			TaskID: scan.TaskID, Mailbox: scan.Mailbox,
			Base: scan.Base, DispatchCommit: scan.DispatchCommit,
		}
		_, err := db.UpsertGitAgentTask(t.Context(), partial)
		require.NoError(t, err)
		detail, ok, err := db.GetGitAgentTask(t.Context(), scan.Mailbox, scan.TaskID)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, "worker-01", detail.Task.Agent)
		assert.Equal(t, "prod-pool", detail.Task.Backend)
	})

	t.Run("a concluded task is not dragged back to running", func(t *testing.T) {
		require.NoError(t, db.ConcludeGitAgentTask(
			t.Context(), id, GitAgentTaskAccepted, GitAgentVerdictAccepted, "captain/task-1", time.Now().UTC()))

		// The watcher cannot tell from the directory that the task finished, so
		// it keeps reporting "running" on every rescan.
		running := scan
		running.Status = GitAgentTaskRunning
		_, err := db.UpsertGitAgentTask(t.Context(), running)
		require.NoError(t, err)

		detail, ok, err := db.GetGitAgentTask(t.Context(), scan.Mailbox, scan.TaskID)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, GitAgentTaskAccepted, detail.Task.Status)
		require.NotNil(t, detail.Task.FinalStatus)
		assert.Equal(t, GitAgentVerdictAccepted, *detail.Task.FinalStatus)
		assert.Equal(t, "captain/task-1", detail.Task.IntegratedBranch)
	})
}

func TestGitAgentAttemptsRecordBothTiers(t *testing.T) {
	handle := dbtest.ForT(t, dbtest.Options{Name: "captain_git_agent_attempts"})
	db, err := Open(t.Context(), WithDSN(handle.DSN()), WithMigrations())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	id, err := db.UpsertGitAgentTask(t.Context(), UpsertGitAgentTaskInput{
		TaskID: "task-1", Mailbox: "mailboxes/aaa.git", Base: "main", DispatchCommit: "deadbeef",
	})
	require.NoError(t, err)

	rejected := RecordGitAgentAttemptInput{
		TaskID: id, Attempt: 1, Tier: "supervisor", Status: GitAgentVerdictRejected,
		Findings: []map[string]any{{"hook": "verify", "kind": "exec", "message": "make lint failed"}},
		Feedback: "fix the lint error",
	}
	require.NoError(t, db.RecordGitAgentAttempt(t.Context(), rejected))
	// Both tiers reach their own verdict on the same attempt.
	require.NoError(t, db.RecordGitAgentAttempt(t.Context(), RecordGitAgentAttemptInput{
		TaskID: id, Attempt: 1, Tier: "sidecar", Status: GitAgentVerdictAccepted,
	}))
	// Re-scanning the verdicts directory must not duplicate them.
	require.NoError(t, db.RecordGitAgentAttempt(t.Context(), rejected))

	detail, ok, err := db.GetGitAgentTask(t.Context(), "mailboxes/aaa.git", "task-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, detail.Attempts, 2)
	assert.Equal(t, "sidecar", detail.Attempts[0].Tier, "attempts sort by attempt then tier")
	assert.Equal(t, "supervisor", detail.Attempts[1].Tier)
	assert.Equal(t, GitAgentVerdictRejected, detail.Attempts[1].Status)
	require.Len(t, detail.Attempts[1].Findings, 1)
	assert.Equal(t, "make lint failed", detail.Attempts[1].Findings[0]["message"])
}

// A task id is unique only within its mailbox — one endpoint routes many
// repositories — so an unscoped lookup that matches two rows must say so rather
// than silently return whichever sorted first.
func TestGetGitAgentTaskRefusesAnAmbiguousTaskID(t *testing.T) {
	handle := dbtest.ForT(t, dbtest.Options{Name: "captain_git_agent_ambiguous"})
	db, err := Open(t.Context(), WithDSN(handle.DSN()), WithMigrations())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	for _, mailbox := range []string{"mailboxes/aaa.git", "mailboxes/bbb.git"} {
		_, err := db.UpsertGitAgentTask(t.Context(), UpsertGitAgentTaskInput{
			TaskID: "task-1", Mailbox: mailbox, Base: "main", DispatchCommit: "deadbeef",
		})
		require.NoError(t, err)
	}

	_, _, err = db.GetGitAgentTask(t.Context(), "", "task-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "more than one mailbox")

	detail, ok, err := db.GetGitAgentTask(t.Context(), "mailboxes/bbb.git", "task-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "mailboxes/bbb.git", detail.Task.Mailbox)
}

// persistPromptRun writes the prompt_runs row only after the run finishes, by
// which time the remote task has already concluded — so the task always lands
// first and the link is resolved on a later pass.
func TestGitAgentTasksLinkToPromptRunsAfterTheFact(t *testing.T) {
	handle := dbtest.ForT(t, dbtest.Options{Name: "captain_git_agent_link"})
	db, err := Open(t.Context(), WithDSN(handle.DSN()), WithMigrations())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = db.UpsertGitAgentTask(t.Context(), UpsertGitAgentTaskInput{
		TaskID: "task-1", Mailbox: "mailboxes/aaa.git", Base: "main",
		DispatchCommit: "deadbeef", AdmissionKey: "run-key-1",
	})
	require.NoError(t, err)

	// Nothing to link yet: the run row does not exist.
	linked, err := db.LinkGitAgentTasksToPromptRuns(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int64(0), linked)

	session, err := db.CreateOrGetSession(t.Context(), CreateSessionInput{
		Source: "claude", ProviderSessionID: "session-1",
	})
	require.NoError(t, err)
	run, err := db.CreatePromptRun(t.Context(), CreatePromptRunInput{
		SessionID: session.ID, RootSessionID: &session.ID, AdmissionKey: "run-key-1",
	})
	require.NoError(t, err)

	linked, err = db.LinkGitAgentTasksToPromptRuns(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int64(1), linked)

	detail, ok, err := db.GetGitAgentTask(t.Context(), "mailboxes/aaa.git", "task-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, detail.Task.PromptRunID)
	assert.Equal(t, run.ID, *detail.Task.PromptRunID)

	// Re-running the linker is a no-op rather than rewriting the same rows.
	linked, err = db.LinkGitAgentTasksToPromptRuns(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int64(0), linked)
}

func TestListGitAgentTasksFilters(t *testing.T) {
	handle := dbtest.ForT(t, dbtest.Options{Name: "captain_git_agent_list"})
	db, err := Open(t.Context(), WithDSN(handle.DSN()), WithMigrations())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	base := time.Now().UTC().Add(-time.Hour)
	for i, spec := range []struct {
		task, agent string
		status      GitAgentTaskStatus
	}{
		{"task-1", "worker-01", GitAgentTaskAccepted},
		{"task-2", "worker-02", GitAgentTaskRejected},
		{"task-3", "worker-01", GitAgentTaskRunning},
	} {
		_, err := db.UpsertGitAgentTask(t.Context(), UpsertGitAgentTaskInput{
			TaskID: spec.task, Mailbox: "mailboxes/aaa.git", Base: "main",
			DispatchCommit: "deadbeef", Backend: "prod-pool", Agent: spec.agent,
			Status: spec.status, DispatchedAt: base.Add(time.Duration(i) * time.Minute),
		})
		require.NoError(t, err)
	}

	all, err := db.ListGitAgentTasks(t.Context(), ListGitAgentTasksFilter{})
	require.NoError(t, err)
	require.Len(t, all, 3)
	assert.Equal(t, "task-3", all[0].TaskID, "newest dispatch first")

	byAgent, err := db.ListGitAgentTasks(t.Context(), ListGitAgentTasksFilter{Agent: "worker-01"})
	require.NoError(t, err)
	assert.Len(t, byAgent, 2)

	byStatus, err := db.ListGitAgentTasks(t.Context(), ListGitAgentTasksFilter{Status: GitAgentTaskRejected})
	require.NoError(t, err)
	require.Len(t, byStatus, 1)
	assert.Equal(t, "task-2", byStatus[0].TaskID)

	none, err := db.ListGitAgentTasks(t.Context(), ListGitAgentTasksFilter{Backend: "other-pool"})
	require.NoError(t, err)
	assert.Empty(t, none)
	assert.NotNil(t, none, "an empty result must marshal as [] rather than null")
}
