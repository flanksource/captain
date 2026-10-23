package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/promptrun"
	"github.com/google/uuid"
)

// chatLifecycleAttempts bounds the optimistic-concurrency retries of a chat's
// terminal lifecycle write: the monitor may advance a session's state version
// between the read and the write.
const chatLifecycleAttempts = 3

// record files one run of the chat and remembers the sessions it was filed on
// and how it ended, which is what endSessions settles them to.
func (c *chatSession) record(rendered PromptRenderResult, runID string, done promptrun.Completed) error {
	run, err := recordCompletedRun(context.Background(), c.recording(rendered, runID), done)
	if err != nil || run == nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastOutcome = run.State
	for _, id := range []*uuid.UUID{&run.SessionID, run.ExecutionSessionID} {
		if id != nil && !slices.Contains(c.sessionIDs, *id) {
			c.sessionIDs = append(c.sessionIDs, *id)
		}
	}
	return nil
}

// endSessions writes the terminal lifecycle of the sessions the chat's runs
// were filed on. Chat runs leave them running between turns (migration 84),
// so the chat's end is what finishes them: cancelled when the person stopped
// it, failed when cause ended it, otherwise as its last run ended.
func (c *chatSession) endSessions(cause error) error {
	c.mu.Lock()
	ids, last := slices.Clone(c.sessionIDs), c.lastOutcome
	c.mu.Unlock()
	if len(ids) == 0 {
		return nil
	}
	lifecycle, reason := chatEndLifecycle(last, cause, c.stream.wasStopped())
	ctx := context.Background()
	db, err := captainDefaultDB(ctx)
	if err != nil {
		return fmt.Errorf("end the sessions of chat %s: %w", c.runID, err)
	}
	var errs []error
	for _, id := range ids {
		if err := updateChatSessionLifecycle(ctx, db, id, lifecycle, reason); err != nil {
			errs = append(errs, fmt.Errorf("end session %s of chat %s as %s: %w", id, c.runID, lifecycle, err))
		}
	}
	return errors.Join(errs...)
}

func chatEndLifecycle(last database.PromptRunState, cause error, stopped bool) (database.SessionLifecycleStatus, string) {
	switch {
	case stopped:
		return database.SessionLifecycleCancelled, "stopped"
	case cause != nil:
		return database.SessionLifecycleFailed, cause.Error()
	}
	return database.SessionLifecycleStatus(last), ""
}

func updateChatSessionLifecycle(ctx context.Context, db *database.DB, id uuid.UUID, lifecycle database.SessionLifecycleStatus, reason string) error {
	var err error
	for range chatLifecycleAttempts {
		if _, err = db.UpdateSessionLifecycle(ctx, id, lifecycle, reason); !errors.Is(err, database.ErrSessionConflict) {
			return err
		}
	}
	return err
}
