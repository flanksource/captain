package approval

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
)

// ErrInvalidResolution reports a decision that cannot be recorded as asked.
var ErrInvalidResolution = errors.New("invalid approval resolution")

// ResolveInput is one person's answer to one durable tool approval.
type ResolveInput struct {
	RequestID uuid.UUID // captain_turn_requests.id; required
	// SessionID scopes the answer to the session the host believes the request
	// belongs to. uuid.Nil takes the session from the request itself, for a host
	// that addresses approvals by ID alone.
	SessionID uuid.UUID
	// ExpectedTurnID, for a caller-tool approval, is the turn that must still be
	// active for the answer to count.
	ExpectedTurnID *uuid.UUID
	Approved       bool
	ResolvedBy     string         // who answered, for the audit trail; required
	Reason         string         // on a denial, fed back to the agent as the message
	UpdatedInput   map[string]any // on an approval, replaces the tool input; carries answers and form content
	// Interrupt, on a denial, also ends the agent's turn.
	Interrupt bool
	// Scope widens an approval where the request offers it.
	Scope api.ApprovalScope
	// Grants is the approved subset of a permissions request.
	Grants *api.NativeSandboxPolicy
}

func (input ResolveInput) decision() api.ApprovalDecision {
	return api.ApprovalDecision{
		Allow: input.Approved, Message: input.Reason, UpdatedInput: input.UpdatedInput,
		Interrupt: input.Interrupt, Scope: input.Scope, Grants: input.Grants,
	}
}

// Resolve records the answer to a pending tool approval. The broker waiting on
// it — in this process or another — reads the decision off the row and hands it
// to the tool call.
//
// No run-state transition follows here: the store only accepts a credential-less
// answer while the run is waiting, and the waiting broker releases the run once
// the last of its approvals is answered. Releasing from Resolve instead would
// un-wait a run that is waiting for something else — an `ask` for a person's
// answer holds the run in waiting with no approval pending at all.
func Resolve(ctx context.Context, db *database.DB, input ResolveInput) (*database.TurnRequest, error) {
	if err := input.validate(); err != nil {
		return nil, err
	}
	row, err := db.GetTurnRequest(ctx, input.RequestID)
	if err != nil {
		return nil, err
	}
	sessionID := input.SessionID
	if sessionID == uuid.Nil {
		sessionID = row.SessionID
	}
	request, err := storedRequest(row)
	if err != nil {
		return nil, err
	}
	// Refused here, before the row goes terminal: a decision the provider cannot
	// translate would otherwise surface as a failed run, not as a bad answer.
	if err := input.decision().Validate(request); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidResolution, err)
	}
	return db.ResolveToolApprovalRequest(ctx, database.ResolveToolApprovalRequestInput{
		SessionID: sessionID, RequestID: input.RequestID, ExpectedTurnID: input.ExpectedTurnID, Approved: input.Approved,
		UpdatedInput: input.UpdatedInput, ResolvedBy: input.ResolvedBy, Reason: input.Reason,
		Interrupt: input.Interrupt, Scope: input.Scope, Grants: input.Grants,
	})
}

func (input ResolveInput) validate() error {
	var problems []string
	if input.RequestID == uuid.Nil {
		problems = append(problems, "a request ID is required")
	}
	if strings.TrimSpace(input.ResolvedBy) == "" {
		problems = append(problems, "resolved by is required to attribute the answer")
	}
	if !input.Approved && input.UpdatedInput != nil {
		problems = append(problems, "a denied approval cannot replace the tool input")
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidResolution, strings.Join(problems, "; "))
	}
	return nil
}

// CancelPending ends every pending turn request one prompt run holds in one
// session, recording reason on each. Every broker waiting on one of them wakes,
// reports the approval unanswered with that reason, and releases the run.
//
// A caller that is ending the run should write its terminal state first: the
// waits' release then leaves the finished run alone instead of passing it back
// through running on its way to the terminal state.
func CancelPending(ctx context.Context, db *database.DB, sessionID, promptRunID uuid.UUID, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: cancelling pending requests needs a reason to report to their waits", database.ErrTurnRequestInvalid)
	}
	return db.CancelPendingTurnRequests(ctx, sessionID, promptRunID, reason)
}
