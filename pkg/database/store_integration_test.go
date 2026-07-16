package database

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	commonsdb "github.com/flanksource/commons-db/db"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDurableSessionPromptRunAndPlanStores(t *testing.T) {
	if os.Getenv("CAPTAIN_DB_EMBEDDED_TEST") == "" {
		t.Skip("set CAPTAIN_DB_EMBEDDED_TEST=1 to run embedded-postgres store tests")
	}

	dsn, stop, err := commonsdb.StartEmbedded(commonsdb.EmbeddedConfig{
		DataDir:  filepath.Join(t.TempDir(), "postgres"),
		Database: "captain_durable_stores",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stop()) })

	db, err := Open(t.Context(), Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	session, err := db.CreateOrGetSession(t.Context(), CreateSessionInput{
		ProviderSessionID: "provider-session-1",
		Source:            "codex",
		Provider:          "openai",
		HostID:            "test-host",
		CWD:               "/tmp/project",
		Title:             "Durable plan",
	})
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, session.ID)
	require.False(t, session.StateObservedAt.IsZero())
	assert.WithinDuration(t, time.Now().UTC(), session.StateObservedAt, 5*time.Second)
	assert.Equal(t, SessionLifecycleCreated, session.LifecycleStatus)
	assert.Equal(t, SessionActivityIdle, session.ActivityState)
	assert.Equal(t, SessionHealthHealthy, session.HealthState)

	replayedSession, err := db.CreateOrGetSession(t.Context(), CreateSessionInput{
		ProviderSessionID: "provider-session-1",
		Source:            "codex",
		Provider:          "openai",
		HostID:            "test-host",
		Title:             "a retry must not overwrite metadata",
	})
	require.NoError(t, err)
	assert.Equal(t, session.ID, replayedSession.ID)
	assert.Equal(t, "Durable plan", replayedSession.Title)
	_, err = db.CreateOrGetSession(t.Context(), CreateSessionInput{
		ID: uuid.New(), ProviderSessionID: "provider-session-1",
		Source: "codex", Provider: "openai", HostID: "test-host",
	})
	assert.ErrorIs(t, err, ErrSessionConflict)

	byProviderID, err := db.GetSessionByIdentity(t.Context(), "provider-session-1", "codex", "openai", "test-host")
	require.NoError(t, err)
	assert.Equal(t, session.ID, byProviderID.ID)
	byUUID, err := db.GetSessionByIdentity(t.Context(), session.ID.String(), "", "", "")
	require.NoError(t, err)
	assert.Equal(t, session.ID, byUUID.ID)
	for name, filters := range map[string][3]string{
		"source":   {"claude", "", ""},
		"provider": {"", "anthropic", ""},
		"host":     {"", "", "another-host"},
	} {
		t.Run("UUID identity enforces "+name+" filter", func(t *testing.T) {
			_, err := db.GetSessionByIdentity(t.Context(), session.ID.String(), filters[0], filters[1], filters[2])
			assert.ErrorIs(t, err, ErrSessionNotFound)
		})
	}

	running := SessionLifecycleRunning
	working := SessionActivityWorking
	updatedSession, err := db.UpdateSessionState(t.Context(), UpdateSessionStateInput{
		ID: session.ID, ExpectedVersion: session.StateVersion,
		LifecycleStatus: &running, ActivityState: &working,
	})
	require.NoError(t, err)
	assert.EqualValues(t, 1, updatedSession.StateVersion)
	assert.NotNil(t, updatedSession.StartedAt)
	updatedProviderID := "provider-session-1"
	sessionUpdatedAtBeforeReplay := updatedSession.UpdatedAt
	sessionObservedAtBeforeReplay := updatedSession.StateObservedAt
	var sessionActivityBeforeReplay sql.NullTime
	require.NoError(t, db.Gorm().Raw(`SELECT last_activity_at FROM captain_sessions WHERE id = ?`, session.ID).
		Scan(&sessionActivityBeforeReplay).Error)
	updatedSession, err = db.UpdateSessionState(t.Context(), UpdateSessionStateInput{
		ID: updatedSession.ID, ExpectedVersion: updatedSession.StateVersion,
		ProviderSessionID: &updatedProviderID, LifecycleStatus: &running, ActivityState: &working,
	})
	require.NoError(t, err)
	assert.Equal(t, updatedProviderID, updatedSession.ProviderSessionID)
	assert.EqualValues(t, 1, updatedSession.StateVersion, "idempotent identity updates must not masquerade as state changes")
	assert.Equal(t, sessionUpdatedAtBeforeReplay, updatedSession.UpdatedAt)
	assert.Equal(t, sessionObservedAtBeforeReplay, updatedSession.StateObservedAt)
	var sessionActivityAfterReplay sql.NullTime
	require.NoError(t, db.Gorm().Raw(`SELECT last_activity_at FROM captain_sessions WHERE id = ?`, session.ID).
		Scan(&sessionActivityAfterReplay).Error)
	assert.Equal(t, sessionActivityBeforeReplay, sessionActivityAfterReplay)
	replacementProviderID := "provider-session-1-replacement"
	_, err = db.UpdateSessionState(t.Context(), UpdateSessionStateInput{
		ID: updatedSession.ID, ExpectedVersion: updatedSession.StateVersion,
		ProviderSessionID: &replacementProviderID,
	})
	assert.ErrorIs(t, err, ErrSessionConflict)
	emptyProviderID := "  "
	_, err = db.UpdateSessionState(t.Context(), UpdateSessionStateInput{
		ID: updatedSession.ID, ExpectedVersion: updatedSession.StateVersion,
		ProviderSessionID: &emptyProviderID,
	})
	assert.ErrorIs(t, err, ErrInvalidSession)
	_, err = db.UpdateSessionState(t.Context(), UpdateSessionStateInput{
		ID: session.ID, ExpectedVersion: session.StateVersion, LifecycleStatus: &running,
	})
	assert.ErrorIs(t, err, ErrSessionConflict)

	t.Run("provider identity binding is set-once under concurrency", func(t *testing.T) {
		unbound, err := db.CreateOrGetSession(t.Context(), CreateSessionInput{
			Source: "codex", Provider: "openai", HostID: "binding-race",
		})
		require.NoError(t, err)
		candidates := []string{"provider-race-a", "provider-race-b"}
		type bindingResult struct {
			value string
			err   error
		}
		start := make(chan struct{})
		results := make(chan bindingResult, len(candidates))
		var wg sync.WaitGroup
		for _, candidate := range candidates {
			wg.Add(1)
			go func(candidate string) {
				defer wg.Done()
				<-start
				_, err := db.UpdateSessionState(t.Context(), UpdateSessionStateInput{
					ID: unbound.ID, ExpectedVersion: unbound.StateVersion,
					ProviderSessionID: &candidate,
				})
				results <- bindingResult{value: candidate, err: err}
			}(candidate)
		}
		close(start)
		wg.Wait()
		close(results)
		var winner string
		var conflicts int
		for result := range results {
			if result.err == nil {
				winner = result.value
				continue
			}
			assert.ErrorIs(t, result.err, ErrSessionConflict)
			conflicts++
		}
		assert.NotEmpty(t, winner)
		assert.Equal(t, 1, conflicts)
		bound, err := db.GetSession(t.Context(), unbound.ID)
		require.NoError(t, err)
		assert.Equal(t, winner, bound.ProviderSessionID)
		assert.EqualValues(t, unbound.StateVersion, bound.StateVersion)
		_, err = db.UpdateSessionState(t.Context(), UpdateSessionStateInput{
			ID: bound.ID, ExpectedVersion: bound.StateVersion, ProviderSessionID: &winner,
		})
		require.NoError(t, err, "an exact binding replay must be idempotent")
	})

	collisionOwner, err := db.CreateOrGetSession(t.Context(), CreateSessionInput{
		ProviderSessionID: "provider-binding-collision", Source: "codex", Provider: "openai", HostID: "collision-host",
	})
	require.NoError(t, err)
	require.NotNil(t, collisionOwner)
	collisionTarget, err := db.CreateOrGetSession(t.Context(), CreateSessionInput{
		Source: "codex", Provider: "openai", HostID: "collision-host",
	})
	require.NoError(t, err)
	collisionID := "provider-binding-collision"
	_, err = db.UpdateSessionState(t.Context(), UpdateSessionStateInput{
		ID: collisionTarget.ID, ExpectedVersion: collisionTarget.StateVersion, ProviderSessionID: &collisionID,
	})
	assert.ErrorIs(t, err, ErrSessionConflict)

	otherRoot, err := db.CreateOrGetSession(t.Context(), CreateSessionInput{Source: "host", Provider: "test"})
	require.NoError(t, err)
	child, err := db.CreateOrGetSession(t.Context(), CreateSessionInput{
		Source: "codex", Provider: "openai", ParentSessionID: &session.ID,
	})
	require.NoError(t, err)
	require.NotNil(t, child.RootSessionID)
	assert.Equal(t, session.ID, *child.RootSessionID)
	grandchild, err := db.CreateOrGetSession(t.Context(), CreateSessionInput{
		Source: "codex", Provider: "openai", ParentSessionID: &child.ID,
	})
	require.NoError(t, err)
	require.NotNil(t, grandchild.RootSessionID)
	assert.Equal(t, session.ID, *grandchild.RootSessionID)
	_, err = db.CreateOrGetSession(t.Context(), CreateSessionInput{
		Source: "codex", Provider: "openai", RootSessionID: &child.ID,
	})
	assert.ErrorIs(t, err, ErrSessionConflict, "an explicit aggregate root must identify a canonical root")
	explicitRootMember, err := db.CreateOrGetSession(t.Context(), CreateSessionInput{
		Source: "codex", Provider: "openai", RootSessionID: &session.ID,
	})
	require.NoError(t, err)
	require.NotNil(t, explicitRootMember.RootSessionID)
	assert.Equal(t, session.ID, *explicitRootMember.RootSessionID)
	_, err = db.CreateOrGetSession(t.Context(), CreateSessionInput{
		Source: "codex", Provider: "openai", ParentSessionID: &child.ID, RootSessionID: &otherRoot.ID,
	})
	assert.ErrorIs(t, err, ErrSessionConflict)
	_, err = db.CreatePromptRun(t.Context(), CreatePromptRunInput{
		SessionID: grandchild.ID, RootSessionID: &otherRoot.ID,
	})
	assert.ErrorIs(t, err, ErrInvalidPromptRun)
	otherParentRun, err := db.CreatePromptRun(t.Context(), CreatePromptRunInput{
		SessionID: otherRoot.ID, AdmissionKey: "unrelated-parent-run",
	})
	require.NoError(t, err)
	finished := PromptRunPhaseFinished
	succeeded := PromptRunStateSucceeded
	_, err = db.UpdatePromptRun(t.Context(), UpdatePromptRunInput{
		ID: otherParentRun.ID, ExpectedVersion: otherParentRun.Version, Phase: &finished, State: &succeeded,
	})
	require.NoError(t, err)
	_, err = db.CreatePromptRun(t.Context(), CreatePromptRunInput{
		SessionID: grandchild.ID, ParentRunID: &otherParentRun.ID,
	})
	assert.ErrorIs(t, err, ErrPromptRunConflict)
	emptyParentRunID := uuid.Nil
	_, err = db.CreatePromptRun(t.Context(), CreatePromptRunInput{
		SessionID: grandchild.ID, ParentRunID: &emptyParentRunID,
	})
	assert.ErrorIs(t, err, ErrInvalidPromptRun)
	selfRunID := uuid.New()
	_, err = db.CreatePromptRun(t.Context(), CreatePromptRunInput{
		ID: selfRunID, SessionID: grandchild.ID, ParentRunID: &selfRunID,
	})
	assert.ErrorIs(t, err, ErrInvalidPromptRun)
	childRun, err := db.CreatePromptRun(t.Context(), CreatePromptRunInput{
		SessionID: grandchild.ID, AdmissionKey: "nested-child-run",
	})
	require.NoError(t, err)
	assert.Equal(t, session.ID, childRun.RootSessionID, "child runs must use their aggregate root")
	_, err = db.CreatePromptRun(t.Context(), CreatePromptRunInput{
		SessionID: explicitRootMember.ID, AdmissionKey: "same-root-active-run",
	})
	assert.ErrorIs(t, err, ErrPromptRunConflict, "all members of an aggregate share the active-run uniqueness boundary")
	_, err = db.UpdatePromptRun(t.Context(), UpdatePromptRunInput{
		ID: childRun.ID, ExpectedVersion: childRun.Version, Phase: &finished, State: &succeeded,
	})
	require.NoError(t, err)

	run, err := db.CreatePromptRun(t.Context(), CreatePromptRunInput{
		SessionID: session.ID, ParentRunID: &childRun.ID, AdmissionKey: "issue-1:planning", Origin: "test",
		RenderedSpec: map[string]any{"goal": "persist plans"},
	})
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, run.ID)
	require.False(t, run.QueuedAt.IsZero())
	assert.WithinDuration(t, time.Now().UTC(), run.QueuedAt, 5*time.Second)
	assert.Equal(t, session.ID, run.RootSessionID)
	require.NotNil(t, run.ParentRunID)
	assert.Equal(t, childRun.ID, *run.ParentRunID)

	replayedRun, err := db.CreatePromptRun(t.Context(), CreatePromptRunInput{
		SessionID: session.ID, AdmissionKey: "issue-1:planning",
	})
	require.NoError(t, err)
	assert.Equal(t, run.ID, replayedRun.ID)
	_, err = db.CreatePromptRun(t.Context(), CreatePromptRunInput{
		ID: uuid.New(), SessionID: session.ID, AdmissionKey: "issue-1:planning",
	})
	assert.ErrorIs(t, err, ErrPromptRunConflict)

	generate := PromptRunPhaseGenerate
	runState := PromptRunStateRunning
	resultJSON := map[string]any{"attempt": float64(1)}
	run, err = db.UpdatePromptRun(t.Context(), UpdatePromptRunInput{
		ID: run.ID, ExpectedVersion: run.Version, Phase: &generate, State: &runState, ResultJSON: &resultJSON,
	})
	require.NoError(t, err)
	assert.EqualValues(t, 1, run.Version)
	assert.NotNil(t, run.StartedAt)
	assert.Equal(t, float64(1), run.ResultJSON["attempt"])
	runUpdatedAtBeforeReplay := run.UpdatedAt
	var runSessionActivityBeforeReplay sql.NullTime
	require.NoError(t, db.Gorm().Raw(`SELECT last_activity_at FROM captain_sessions WHERE id = ?`, session.ID).
		Scan(&runSessionActivityBeforeReplay).Error)
	replayedRunState, err := db.UpdatePromptRun(t.Context(), UpdatePromptRunInput{
		ID: run.ID, ExpectedVersion: run.Version, Phase: &generate, State: &runState, ResultJSON: &resultJSON,
	})
	require.NoError(t, err)
	assert.Equal(t, runUpdatedAtBeforeReplay, replayedRunState.UpdatedAt)
	assert.Equal(t, run.Version, replayedRunState.Version)
	var runSessionActivityAfterReplay sql.NullTime
	require.NoError(t, db.Gorm().Raw(`SELECT last_activity_at FROM captain_sessions WHERE id = ?`, session.ID).
		Scan(&runSessionActivityAfterReplay).Error)
	assert.Equal(t, runSessionActivityBeforeReplay, runSessionActivityAfterReplay)
	_, err = db.UpdatePromptRun(t.Context(), UpdatePromptRunInput{
		ID: run.ID, ExpectedVersion: 0, State: &runState,
	})
	assert.ErrorIs(t, err, ErrPromptRunConflict)

	validTurnID := uuid.New()
	require.NoError(t, db.Gorm().Exec(`INSERT INTO captain_turns (id, session_id, turn_index) VALUES (?, ?, 0)`,
		validTurnID, session.ID).Error)
	crossSessionTurnID := uuid.New()
	require.NoError(t, db.Gorm().Exec(`INSERT INTO captain_turns (id, session_id, turn_index) VALUES (?, ?, 0)`,
		crossSessionTurnID, otherRoot.ID).Error)
	_, err = db.CreateOrGetPlan(t.Context(), CreatePlanInput{
		SourceSessionID: session.ID, SourcePromptRunID: &run.ID,
		SourceTurnID: &crossSessionTurnID, Variant: "cross-session-turn",
	})
	assert.ErrorIs(t, err, ErrPlanConflict)
	plan, err := db.CreateOrGetPlan(t.Context(), CreatePlanInput{
		SourceSessionID: session.ID, SourcePromptRunID: &run.ID,
		SourceTurnID: &validTurnID, Variant: "safe", Title: "Safe migration", Path: "/tmp/deleted-plan.md",
	})
	require.NoError(t, err)
	require.NotNil(t, plan.SourceTurnID)
	assert.Equal(t, validTurnID, *plan.SourceTurnID)
	replayedPlan, err := db.CreateOrGetPlan(t.Context(), CreatePlanInput{
		SourceSessionID: session.ID, SourcePromptRunID: &run.ID, Variant: "safe",
		Title: "retry must not overwrite metadata",
	})
	require.NoError(t, err)
	assert.Equal(t, plan.ID, replayedPlan.ID)
	assert.Equal(t, "Safe migration", replayedPlan.Title)
	_, err = db.CreateOrGetPlan(t.Context(), CreatePlanInput{
		ID: uuid.New(), SourceSessionID: session.ID, SourcePromptRunID: &run.ID, Variant: "safe",
	})
	assert.ErrorIs(t, err, ErrPlanConflict)

	first, err := db.AppendPlanRevision(t.Context(), AppendPlanRevisionInput{
		PlanID: plan.ID, PlanMarkdown: "\r\n# Plan\r\n\r\n- preserve data\r\n", CreatedBy: "test",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, first.Revision)
	replayedRevision, err := db.AppendPlanRevision(t.Context(), AppendPlanRevisionInput{
		PlanID: plan.ID, PlanMarkdown: "# Plan\n\n- preserve data\n",
	})
	require.NoError(t, err)
	assert.Equal(t, first.ID, replayedRevision.ID)

	const concurrentRevisions = 4
	var wg sync.WaitGroup
	results := make(chan *PlanRevision, concurrentRevisions)
	errorsCh := make(chan error, concurrentRevisions)
	for i := range concurrentRevisions {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			revision, err := db.AppendPlanRevision(t.Context(), AppendPlanRevisionInput{
				PlanID: plan.ID, PlanMarkdown: fmt.Sprintf("# Plan\n\nvariant %d", i),
			})
			if err != nil {
				errorsCh <- err
				return
			}
			results <- revision
		}(i)
	}
	wg.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		require.NoError(t, err)
	}
	var revisionNumbers []int
	var latest *PlanRevision
	for revision := range results {
		revisionNumbers = append(revisionNumbers, revision.Revision)
		if latest == nil || revision.Revision > latest.Revision {
			latest = revision
		}
	}
	sort.Ints(revisionNumbers)
	assert.Equal(t, []int{2, 3, 4, 5}, revisionNumbers)
	require.NotNil(t, latest)

	otherPlan, err := db.CreateOrGetPlan(t.Context(), CreatePlanInput{
		SourceSessionID: session.ID, SourcePromptRunID: &run.ID, Variant: "fast",
	})
	require.NoError(t, err)
	otherRevision, err := db.AppendPlanRevision(t.Context(), AppendPlanRevisionInput{
		PlanID: otherPlan.ID, PlanMarkdown: "# Other plan",
	})
	require.NoError(t, err)
	_, err = db.ApprovePlanRevision(t.Context(), ApprovePlanRevisionInput{
		PlanID: plan.ID, RevisionID: otherRevision.ID,
	})
	assert.ErrorIs(t, err, ErrPlanConflict)

	approved, err := db.ApprovePlanRevision(t.Context(), ApprovePlanRevisionInput{
		PlanID: plan.ID, RevisionID: latest.ID, ApprovedBy: "reviewer", Comment: "ship it",
	})
	require.NoError(t, err)
	require.NotNil(t, approved.ApprovedRevision)
	assert.Equal(t, latest.ID, approved.ApprovedRevision.ID)
	assert.Equal(t, latest.PlanMarkdown, approved.ApprovedRevision.PlanMarkdown)
	assert.Equal(t, PlanApprovalApproved, approved.ApprovalState)

	approvedRevision, err := db.GetApprovedPlanRevision(t.Context(), plan.ID)
	require.NoError(t, err)
	assert.Equal(t, latest.ID, approvedRevision.ID)
	listedPlans, err := db.ListPlans(t.Context(), PlanFilter{SourcePromptRunID: &run.ID})
	require.NoError(t, err)
	assert.Len(t, listedPlans, 2)

	rollbackErr := errors.New("rollback host link")
	err = db.Transaction(t.Context(), func(tx *DB) error {
		_, err := tx.CreateOrGetPlan(t.Context(), CreatePlanInput{
			SourceSessionID: session.ID, SourcePromptRunID: &run.ID, Variant: "rolled-back",
		})
		require.NoError(t, err)
		return rollbackErr
	})
	assert.ErrorIs(t, err, rollbackErr)
	rolledBack := "rolled-back"
	plansAfterRollback, err := db.ListPlans(t.Context(), PlanFilter{
		SourcePromptRunID: &run.ID, Variant: &rolledBack,
	})
	require.NoError(t, err)
	assert.Empty(t, plansAfterRollback)
}
