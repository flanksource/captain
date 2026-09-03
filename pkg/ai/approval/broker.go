// Package approval brokers durable tool approvals for any Captain execution
// that owns a session and a prompt run.
//
// Broker is the api.PermissionFunc every path shares: it records one pending
// captain_turn_requests row, hands the host an api.EventPermission frame to
// surface, and blocks until that row is resolved, expires, or the caller's
// context ends. The aichat execution path supplies its caller-tool credential,
// turn and model call; a streaming provider run (`captain prompt run`) or an
// external host such as a dashboard supplies none of the three and is
// identified by its prompt run and tool call alone.
package approval

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
)

const (
	// DefaultPoll is how often an unresolved approval is re-read.
	DefaultPoll = 100 * time.Millisecond
	// CallerToolTimeout bounds an approval raised by a caller tool, which a
	// person answers inside a live chat turn.
	CallerToolTimeout = 5 * time.Minute
	// ProviderTimeout bounds an approval raised by a provider, which suspends
	// the run and may be answered long after the process that raised it exited.
	ProviderTimeout = 24 * time.Hour
)

// ErrInvalidBroker reports a Broker that cannot broker anything.
var ErrInvalidBroker = errors.New("invalid approval broker")

// Broker answers tool-permission requests from the durable approval table.
type Broker struct {
	DB          *database.DB
	SessionID   uuid.UUID // captain_sessions.id; required
	PromptRunID uuid.UUID // captain_prompt_runs.id; required

	// TurnID, ModelCallID and CredentialID identify a caller-tool approval
	// raised inside an aichat turn. A provider or host approval leaves all
	// three empty: those executions never open a turn or a model call.
	TurnID       *uuid.UUID
	ModelCallID  *uuid.UUID
	CredentialID uuid.UUID

	RequestedBy string        // who raised it, e.g. "provider" or "caller_tool"
	Timeout     time.Duration // approval expiry; required
	Poll        time.Duration // re-read interval; DefaultPoll when zero

	// Notify receives the EventPermission frame carrying the tool, its input,
	// the provider tool-call ID and the durable approval ID, so the host can
	// surface a request it is expected to answer. Required.
	Notify func(context.Context, api.Event) error

	// OnWaiting and OnRunning bracket the wait with the host's own state
	// transitions. A credential-less approval depends on OnWaiting: the store
	// only resolves one while its prompt run is waiting.
	OnWaiting func(context.Context) error
	OnRunning func(context.Context) error

	// ClaimToolUseID resolves a request whose tool-use ID the runtime generated
	// locally onto the provider's own tool-call ID. Required only when a caller
	// can set PermissionRequest.ToolUseIDGenerated.
	ClaimToolUseID func(context.Context, api.PermissionRequest) (string, error)
}

// Validate reports whether the broker names everything it needs to record and
// surface an approval.
func (b *Broker) Validate() error {
	var missing []string
	if b.DB == nil {
		missing = append(missing, "a database")
	}
	if b.SessionID == uuid.Nil {
		missing = append(missing, "a session ID")
	}
	if b.PromptRunID == uuid.Nil {
		missing = append(missing, "a prompt run ID")
	}
	if b.Notify == nil {
		missing = append(missing, "a notify callback")
	}
	if b.Timeout <= 0 {
		missing = append(missing, "a positive timeout")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: approvals need %s", ErrInvalidBroker, strings.Join(missing, ", "))
	}
	return nil
}

// CanUseTool is the api.PermissionFunc. It records the pending approval
// idempotently, surfaces it, and blocks until it is answered.
//
// OnWaiting and OnRunning bracket the wait: once the host has been told the run
// is waiting, every way out of this function tells it the run is running again.
// The results are named so the deferred half of that bracket can join its error
// onto whichever exit path fired.
func (b *Broker) CanUseTool(
	ctx context.Context,
	req api.PermissionRequest,
) (decision api.PermissionDecision, err error) {
	if err := b.Validate(); err != nil {
		return api.PermissionDecision{}, err
	}
	if req.ToolUseIDGenerated {
		if b.ClaimToolUseID == nil {
			return api.PermissionDecision{}, fmt.Errorf(
				"%w: tool %q generated its own tool-use ID with no ClaimToolUseID to correlate it", ErrInvalidBroker, req.Tool)
		}
		toolUseID, err := b.ClaimToolUseID(ctx, req)
		if err != nil {
			return api.PermissionDecision{}, err
		}
		req.ToolUseID = toolUseID
	}
	pending, err := b.DB.CreateToolApprovalRequest(ctx, database.CreateToolApprovalRequestInput{
		CredentialID: b.CredentialID, SessionID: b.SessionID, PromptRunID: b.PromptRunID,
		TurnID: optionalUUID(b.TurnID), ModelCallID: optionalUUID(b.ModelCallID),
		RequestedBy: b.RequestedBy, ToolCallID: req.ToolUseID, Tool: req.Tool, Input: req.Input,
		ExpiresAt: time.Now().Add(b.Timeout),
	})
	if err != nil {
		return api.PermissionDecision{}, err
	}
	if b.OnWaiting != nil {
		if waitingErr := b.OnWaiting(ctx); waitingErr != nil {
			return api.PermissionDecision{}, waitingErr
		}
	}
	// From here the host believes the run is waiting, so every exit has to put it
	// back — not just the one that reaches a verdict. A Notify that failed used
	// to return straight out, leaving the run parked on an approval no reader was
	// ever shown; and a cancelled caller resumed on its own dead context, so the
	// transition failed exactly when it mattered. context.WithoutCancel is the
	// point: ending the wait is the response to the cancellation, not a victim
	// of it.
	defer func() {
		err = errors.Join(err, b.resume(context.WithoutCancel(ctx)))
	}()
	if notifyErr := b.Notify(ctx, api.Event{
		Kind: api.EventPermission, Tool: req.Tool, ToolCallID: req.ToolUseID,
		ApprovalID: pending.ID.String(), Input: req.Input,
	}); notifyErr != nil {
		return api.PermissionDecision{}, notifyErr
	}
	return b.wait(ctx, pending.ID)
}

func (b *Broker) resume(ctx context.Context) error {
	if b.OnRunning == nil {
		return nil
	}
	return b.OnRunning(ctx)
}

func (b *Broker) wait(ctx context.Context, requestID uuid.UUID) (api.PermissionDecision, error) {
	ticker := time.NewTicker(b.poll())
	defer ticker.Stop()
	for {
		request, err := b.DB.GetTurnRequest(ctx, requestID)
		if err != nil {
			return api.PermissionDecision{}, err
		}
		if decision, resolved, err := decide(request); resolved {
			return decision, err
		}
		if request.ExpiresAt != nil && !time.Now().Before(*request.ExpiresAt) {
			if err := b.DB.ExpireToolApprovalRequest(ctx, request.ID,
				database.TurnRequestStateExpired, "approval timed out"); err != nil {
				return api.PermissionDecision{}, err
			}
			continue
		}
		// A caller-tool approval outlives its credential only as a leak: the
		// credential is the authority the tool would run under.
		if request.CredentialID != nil {
			if err := b.DB.ValidateCallerToolCredential(ctx, *request.CredentialID); err != nil {
				_ = b.DB.ExpireToolApprovalRequest(ctx, request.ID, database.TurnRequestStateCancelled, err.Error())
				return api.PermissionDecision{}, err
			}
		}
		select {
		case <-ctx.Done():
			_ = b.DB.ExpireToolApprovalRequest(context.Background(), request.ID,
				database.TurnRequestStateCancelled, ctx.Err().Error())
			return api.PermissionDecision{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (b *Broker) poll() time.Duration {
	if b.Poll > 0 {
		return b.Poll
	}
	return DefaultPoll
}

// decide maps a terminal approval row onto its decision; the second result
// reports whether the row is terminal at all.
func decide(request *database.TurnRequest) (api.PermissionDecision, bool, error) {
	switch request.State {
	case database.TurnRequestStateApproved:
		decision := api.PermissionDecision{Allow: true}
		if updated, ok := request.Response["updatedInput"].(map[string]any); ok {
			decision.UpdatedInput = updated
		}
		return decision, true, nil
	case database.TurnRequestStateDenied:
		message := request.Reason
		if message == "" {
			message = "tool call denied"
		}
		return api.PermissionDecision{Message: message}, true, nil
	case database.TurnRequestStateExpired, database.TurnRequestStateCancelled:
		return api.PermissionDecision{}, true, fmt.Errorf("tool approval %s", request.State)
	}
	return api.PermissionDecision{}, false, nil
}

func optionalUUID(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
}
