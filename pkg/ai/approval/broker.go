// Package approval brokers durable tool approvals for any Captain execution
// that owns a session and a prompt run.
//
// Broker is the api.ApprovalFunc every path shares: it records one pending
// captain_turn_requests row, holds the prompt run in waiting, hands the host an
// api.EventPermission frame to surface, and blocks until that row is resolved,
// expires, or the caller's context ends — then releases the run once no other
// approval holds it. Resolve and CancelPending are the other side: the only
// writes a host makes to answer or withdraw an approval. The aichat execution path supplies its caller-tool credential,
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
	// It is a ceiling for a host that declares no window of its own, not the
	// answer: a run under a deadline gets the deadline instead.
	ProviderTimeout = 24 * time.Hour
	// DeadlineGrace is how far before a run's own deadline an approval lapses
	// when the deadline is the binding bound.
	//
	// The two clocks report different things. A run that dies on its deadline
	// reports a timeout — an exhausted budget, a cancelled context — naming no
	// tool and no person. Only the approval lapsing first produces an error that
	// says nobody answered. Expiring at exactly the deadline makes which one
	// happens a race, so the approval is pulled in far enough to win it.
	DeadlineGrace = 30 * time.Second
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
	Timeout     time.Duration // longest an approval may stay unanswered; required
	Poll        time.Duration // re-read interval; DefaultPoll when zero

	// Deadline is when the run this approval blocks ends regardless of the
	// answer — its budget timeout, typically. When set it bounds the expiry:
	// Timeout is the ceiling, and whichever of the two comes first decides.
	// Zero leaves the expiry to Timeout and the calling context alone.
	Deadline time.Time

	// Notify receives the EventPermission frame carrying the tool, its input,
	// the provider tool-call ID and the durable approval ID, so the host can
	// surface a request it is expected to answer. Required.
	Notify func(context.Context, api.Event) error

	// OnRunState receives the prompt run row each time the broker moves it
	// between waiting and running. The broker owns that transition — the store
	// only resolves a credential-less approval while its run is waiting — so a
	// host writes nothing. The hook exists for a host that keeps its own copy of
	// the run (aichat's execution names its version on every later update) or
	// mirrors the posture onto state of its own. Optional.
	OnRunState func(context.Context, *database.PromptRun) error

	// ClaimToolUseID resolves a request whose tool-use ID the runtime generated
	// locally onto the provider's own tool-call ID. Required only when a caller
	// can set PermissionRequest.ToolUseIDGenerated.
	ClaimToolUseID func(context.Context, api.ApprovalRequest) (string, error)
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

// OnApproval is the api.ApprovalFunc. It records the pending approval
// idempotently, holds the prompt run in waiting, surfaces the approval, and
// blocks until it is answered.
//
// Once the run is held, every way out of this function releases it again. The
// results are named so the deferred release can join its error onto whichever
// exit path fired.
func (b *Broker) OnApproval(
	ctx context.Context,
	req api.ApprovalRequest,
) (decision api.ApprovalDecision, err error) {
	if err := b.Validate(); err != nil {
		return api.ApprovalDecision{}, err
	}
	if err := req.Validate(); err != nil {
		return api.ApprovalDecision{}, fmt.Errorf("%w: %w", ErrInvalidBroker, err)
	}
	// The durable row stores the answer, so a secret one would land in plain
	// JSON. A host that can keep a secret answers it through a direct callback.
	for _, question := range req.Questions {
		if question.Secret {
			return api.ApprovalDecision{}, fmt.Errorf("question %q is secret and cannot use the durable approval broker", question.ID)
		}
	}
	if req.ToolUseIDGenerated {
		if b.ClaimToolUseID == nil {
			return api.ApprovalDecision{}, fmt.Errorf(
				"%w: tool %q generated its own tool-use ID with no ClaimToolUseID to correlate it", ErrInvalidBroker, req.Tool)
		}
		toolUseID, err := b.ClaimToolUseID(ctx, req)
		if err != nil {
			return api.ApprovalDecision{}, err
		}
		req.ToolUseID = toolUseID
	}
	expiresAt, err := b.expiry(ctx, time.Now(), req.Tool)
	if err != nil {
		return api.ApprovalDecision{}, err
	}
	pending, err := b.DB.CreateToolApprovalRequest(ctx, database.CreateToolApprovalRequestInput{
		CredentialID: b.CredentialID, SessionID: b.SessionID, PromptRunID: b.PromptRunID,
		TurnID: optionalUUID(b.TurnID), ModelCallID: optionalUUID(b.ModelCallID),
		RequestedBy: b.RequestedBy, ToolCallID: req.ToolUseID, Tool: req.Tool, Input: req.Input,
		ExpiresAt: expiresAt, Approval: &req,
	})
	if err != nil {
		return api.ApprovalDecision{}, err
	}
	run, held, err := b.DB.HoldPromptRunForApprovals(ctx, b.PromptRunID)
	if err != nil {
		return api.ApprovalDecision{}, b.abandon(ctx, pending.ID, err)
	}
	// From here the run is waiting, so every exit has to put it back — not just
	// the one that reaches a verdict. A Notify that failed used to return straight
	// out, leaving the run parked on an approval no reader was ever shown; and a
	// cancelled caller released on its own dead context, so the transition failed
	// exactly when it mattered. context.WithoutCancel is the point: ending the
	// wait is the response to the cancellation, not a victim of it.
	defer func() {
		err = errors.Join(err, b.release(context.WithoutCancel(ctx)))
	}()
	if hookErr := b.observe(ctx, run, held); hookErr != nil {
		return api.ApprovalDecision{}, b.abandon(ctx, pending.ID, hookErr)
	}
	if notifyErr := b.Notify(ctx, api.Event{
		Kind: api.EventPermission, Tool: req.Tool, ToolCallID: req.ToolUseID,
		ApprovalID: pending.ID.String(), Input: req.Input, Request: &req,
	}); notifyErr != nil {
		return api.ApprovalDecision{}, b.abandon(ctx, pending.ID, notifyErr)
	}
	return b.wait(ctx, pending.ID)
}

// abandon cancels an approval the broker cannot see through, and returns why.
//
// A request nobody was shown — or one raised on a run that already ended, or
// whose host refused the hold — is not a live question, and leaving the row
// pending would keep the run held in waiting on it: the posture is derived from
// the outstanding set, so the release that follows only lands once the row is
// gone. Ending the row is the same response the cancelled-caller path makes,
// for the same reason.
func (b *Broker) abandon(ctx context.Context, requestID uuid.UUID, cause error) error {
	if cancelErr := b.DB.ExpireToolApprovalRequest(context.WithoutCancel(ctx), requestID,
		database.TurnRequestStateCancelled, cause.Error()); cancelErr != nil {
		return errors.Join(cause, cancelErr)
	}
	return cause
}

// expiry resolves when this approval lapses: the configured window, pulled in to
// sit DeadlineGrace ahead of every deadline the run is already under — the one
// the host declared and the one the calling context carries.
//
// Without the bound the window is decoration. A 24h approval on a run whose
// budget allows 2h expires 22h after the run is already dead, so the run always
// fails on the budget and the failure names a timeout rather than the question
// nobody answered.
//
// A window that has already closed is refused rather than recorded: an approval
// that can only ever expire is worse than none, because it reads to a person as
// a live question they still have time to answer.
func (b *Broker) expiry(ctx context.Context, now time.Time, tool string) (time.Time, error) {
	expiresAt := now.Add(b.Timeout)
	bound := time.Time{}
	for _, deadline := range b.deadlines(ctx) {
		if pulled := deadline.Add(-DeadlineGrace); pulled.Before(expiresAt) {
			expiresAt, bound = pulled, deadline
		}
	}
	if !expiresAt.After(now) {
		return time.Time{}, fmt.Errorf(
			"cannot ask anyone to approve %q: the run's deadline is %s away, less than the %s an approval needs to be raised and answered",
			tool, bound.Sub(now).Round(time.Second), DeadlineGrace)
	}
	return expiresAt, nil
}

func (b *Broker) deadlines(ctx context.Context) []time.Time {
	var deadlines []time.Time
	if !b.Deadline.IsZero() {
		deadlines = append(deadlines, b.Deadline)
	}
	if deadline, ok := ctx.Deadline(); ok {
		deadlines = append(deadlines, deadline)
	}
	return deadlines
}

// release puts the run back to running — but only once nothing else is holding
// it.
//
// A turn that issues parallel tool calls raises one approval per call, each
// waited on by its own goroutine, so several waits overlap on one prompt run.
// Releasing from whichever wait finishes first un-waits a run its siblings are
// still blocking, and that strands them: the store only resolves a
// credential-less approval while its prompt run is waiting, so the host's
// approve button starts returning a conflict and the siblings can no longer end
// any way but expiry.
//
// The posture is therefore derived from the outstanding set, under the run's row
// lock, rather than bracketed around one wait. That also makes the answer true
// for approvals this process never saw — another host, or a sweep, may have
// resolved one while this wait was blocked — and leaves a run a stop already
// ended exactly as the stop left it.
func (b *Broker) release(ctx context.Context) error {
	run, released, err := b.DB.ReleasePromptRunFromApprovals(ctx, b.PromptRunID)
	if err != nil {
		return err
	}
	return b.observe(ctx, run, released)
}

// observe hands the host the row the broker just wrote, when it wrote one.
func (b *Broker) observe(ctx context.Context, run *database.PromptRun, moved bool) error {
	if !moved || b.OnRunState == nil {
		return nil
	}
	return b.OnRunState(ctx, run)
}

func (b *Broker) wait(ctx context.Context, requestID uuid.UUID) (api.ApprovalDecision, error) {
	ticker := time.NewTicker(b.poll())
	defer ticker.Stop()
	for {
		request, err := b.DB.GetTurnRequest(ctx, requestID)
		if err != nil {
			return api.ApprovalDecision{}, err
		}
		if decision, resolved, err := decide(request); resolved {
			if err != nil {
				// The wait ended without an answer — because it lapsed here, or
				// because another writer (the monitor's sweep) took the row while this
				// process was blocked on it. Either way the only trace on the host's
				// narration so far is "awaiting approval", which a reader cannot tell
				// apart from a run still waiting.
				if notifyErr := b.notifyEnded(ctx, request); notifyErr != nil {
					err = errors.Join(err, notifyErr)
				}
			}
			return decision, err
		}
		if request.ExpiresAt != nil && !time.Now().Before(*request.ExpiresAt) {
			if err := b.DB.ExpireToolApprovalRequest(ctx, request.ID,
				database.TurnRequestStateExpired, "approval timed out"); err != nil {
				return api.ApprovalDecision{}, err
			}
			continue
		}
		// A caller-tool approval outlives its credential only as a leak: the
		// credential is the authority the tool would run under.
		if request.CredentialID != nil {
			if err := b.DB.ValidateCallerToolCredential(ctx, *request.CredentialID); err != nil {
				return api.ApprovalDecision{}, b.abandon(ctx, request.ID, err)
			}
		}
		select {
		case <-ctx.Done():
			return api.ApprovalDecision{}, b.abandon(ctx, request.ID, ctx.Err())
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
func decide(request *database.TurnRequest) (api.ApprovalDecision, bool, error) {
	switch request.State {
	case database.TurnRequestStateApproved:
		decision, err := storedDecision(request.Response)
		decision.Allow = true
		return decision, true, err
	case database.TurnRequestStateDenied:
		decision, err := storedDecision(request.Response)
		decision.Message = request.Reason
		if decision.Message == "" {
			decision.Message = "tool call denied"
		}
		return decision, true, err
	case database.TurnRequestStateExpired, database.TurnRequestStateCancelled:
		return api.ApprovalDecision{}, true, unansweredError(request)
	}
	return api.ApprovalDecision{}, false, nil
}

// unansweredError explains an approval that ended without a decision in the
// terms an operator can act on. "tool approval expired" named none of them: not
// the tool, not the durable row to go look at, not how long somebody had to
// answer, and not the fact that a person was asked at all — so the same four
// words covered a 24-hour abandonment and a run stopped a second after it
// started.
func unansweredError(request *database.TurnRequest) error {
	waited := time.Since(request.CreatedAt)
	if request.ResolvedAt != nil {
		waited = request.ResolvedAt.Sub(request.CreatedAt)
	}
	message := fmt.Sprintf("tool approval %s: nobody answered the request to run %q; waited %s (approval %s",
		request.State, toolOf(request), waited.Round(time.Millisecond), request.ID)
	if request.Reason != "" {
		message += "; " + request.Reason
	}
	return errors.New(message + ")")
}

// notifyEnded surfaces the outcome of an approval on the same stream the request
// went out on. An EventPermission carrying a Reason is the answer to the earlier
// frame with the same ApprovalID, not a second ask.
func (b *Broker) notifyEnded(ctx context.Context, request *database.TurnRequest) error {
	reason := string(request.State)
	if request.Reason != "" {
		reason += ": " + request.Reason
	}
	return b.Notify(ctx, api.Event{
		Kind: api.EventPermission, Tool: toolOf(request), ToolCallID: request.ToolCallID,
		ApprovalID: request.ID.String(), Reason: reason,
	})
}

func toolOf(request *database.TurnRequest) string {
	tool, _ := request.Request["tool"].(string)
	return tool
}

func optionalUUID(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
}
