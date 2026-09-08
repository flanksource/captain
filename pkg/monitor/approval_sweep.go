package monitor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
)

// defaultApprovalSweepInterval is how often pending tool approvals are checked
// against their own expiry. It is minutes, not seconds: an approval window is
// measured in minutes to hours, and the sweep exists to bound abandonment, not
// to react promptly.
const defaultApprovalSweepInterval = time.Minute

// approvalSweepStore is the slice of the database a sweep touches. Terminating
// a row goes through the store's existing ExpireToolApprovalRequest — its
// `state = 'pending'` guard is what keeps a sweep from overwriting a decision
// somebody made a moment earlier, and a second expiry path would not have it.
type approvalSweepStore interface {
	ListStaleToolApprovals(ctx context.Context, now time.Time) ([]database.StaleToolApproval, error)
	ExpireToolApprovalRequest(ctx context.Context, id uuid.UUID, state database.TurnRequestState, reason string) error
}

type approvalSweepResult struct {
	expired   int
	cancelled int
	failed    int
}

func (r approvalSweepResult) swept() int { return r.expired + r.cancelled }

// sweepToolApprovals terminates the approvals nobody can answer any more.
//
// The row's own reason is where an operator finds out what happened, so it names
// the tool and how long the question stood: by the time anyone looks, the
// process that asked is gone and this text is all that is left of it.
func sweepToolApprovals(ctx context.Context, store approvalSweepStore, now time.Time) (approvalSweepResult, error) {
	var result approvalSweepResult
	stale, err := store.ListStaleToolApprovals(ctx, now)
	if err != nil {
		return result, err
	}
	var failures []error
	for _, approval := range stale {
		state, reason := sweepVerdict(approval, now)
		if err := store.ExpireToolApprovalRequest(ctx, approval.ID, state, reason); err != nil {
			result.failed++
			failures = append(failures, fmt.Errorf("sweep approval %s: %w", approval.ID, err))
			continue
		}
		if state == database.TurnRequestStateExpired {
			result.expired++
			continue
		}
		result.cancelled++
	}
	return result, errors.Join(failures...)
}

func sweepVerdict(approval database.StaleToolApproval, now time.Time) (database.TurnRequestState, string) {
	if approval.Lapsed {
		return database.TurnRequestStateExpired, fmt.Sprintf(
			"nobody answered the %q approval in the %s it was open",
			approval.Tool, now.Sub(approval.CreatedAt).Round(time.Second))
	}
	return database.TurnRequestStateCancelled, fmt.Sprintf(
		"the prompt run is %s, so the %q approval can no longer be answered",
		approval.RunState, approval.Tool)
}

// sweepApprovals is the monitor's periodic half of the sweep. It runs here
// because the monitor is already the single writer for this database — the
// advisory lock in Run makes exactly one process eligible — which is the
// property a sweeper needs to avoid two hosts racing to terminate the same row.
func (m *Monitor) sweepApprovals(ctx context.Context) {
	result, err := sweepToolApprovals(ctx, m.db, time.Now().UTC())
	if err != nil {
		log.Warnf("tool approval sweep: %v", err)
	}
	if result.swept() > 0 {
		log.Infof("tool approval sweep: expired %d unanswered, cancelled %d whose run had ended",
			result.expired, result.cancelled)
	}
}
