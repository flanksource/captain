package claudeagent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/provider/jsonrpc"
	aitools "github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/clicky/exec"
	"github.com/google/uuid"
)

// turnState carries the channels a single in-flight turn uses to receive mapped
// events from the JSON-RPC notification handler. inbox is the handler->turn
// path; term is closed by the handler on a terminal notification; quit is closed
// by the turn goroutine on exit so a late handler send does not block.
//
// ctx and onApproval let the server-request handler (onRequest, which runs on its
// own goroutine) reach the turn's deadline and permission callback so a
// can_use_tool round-trip is scoped to the turn and unblocks on cancellation.
type turnState struct {
	inbox chan ai.Event
	term  chan struct{}
	quit  chan struct{}

	ctx        context.Context
	onApproval ai.ApprovalFunc
	// toolPolicy is the run's tool policy, checked before the callback so a denied
	// tool is refused without asking anyone.
	toolPolicy aitools.ResolveOptions
	// planMode marks a plan-only turn: ExitPlanMode is then the terminal signal
	// and its can_use_tool is answered by the shared policy, never the broker.
	planMode bool

	promptMu sync.Mutex
	// unsettled holds the uuids of the prompts this turn sent that no SDK result
	// has reported consuming yet. The SDK folds a prompt that arrives mid-turn
	// into the running turn, so one result can settle several prompts; it names
	// them in user_message_uuids.
	unsettled map[string]bool
	// idleWaiting records an idle that arrived while a prompt was unsettled, so
	// withdrawing that prompt can end the turn without waiting for another idle.
	idleWaiting bool
	ended       bool
	// result is the captain turn's result, merged across every SDK result the
	// turn settled before the session went idle.
	result *ai.Event

	interrupting atomic.Bool
}

type promptParams struct {
	// UUID stamps the SDK user message, so the result that consumes the prompt
	// can name it.
	UUID        string             `json:"uuid"`
	Text        string             `json:"text"`
	Attachments []promptAttachment `json:"attachments,omitempty"`
}

type promptAttachment struct {
	MediaType string `json:"mediaType"`
	Data      string `json:"data"`
	Filename  string `json:"filename,omitempty"`
}

func composePrompt(req ai.Request) string {
	return req.Prompt.User
}

func buildPromptParams(req ai.Request, promptID string) (promptParams, error) {
	params := promptParams{UUID: promptID, Text: composePrompt(req), Attachments: make([]promptAttachment, 0, len(req.Prompt.Attachments))}
	for i, attachment := range req.Prompt.Attachments {
		content, ok := attachment.PreparedContent()
		if !ok {
			return promptParams{}, fmt.Errorf("attachment %d (%s) is not prepared", i+1, attachment.ID)
		}
		data := content.Bytes
		if data == nil && content.Path != "" {
			var err error
			data, err = os.ReadFile(content.Path)
			if err != nil {
				return promptParams{}, fmt.Errorf("read prepared attachment %s: %w", attachment.ID, err)
			}
		}
		params.Attachments = append(params.Attachments, promptAttachment{
			MediaType: attachment.MediaType,
			Data:      base64.StdEncoding.EncodeToString(data),
			Filename:  attachment.Filename,
		})
	}
	return params, nil
}

// runTurn owns one turn's event channel: it sends the prompt, forwards mapped
// notifications, and emits the terminal result (or a loud error on cancellation
// / mid-turn process exit).
func (p *Provider) runTurn(ctx context.Context, req ai.Request, events chan ai.Event) {
	defer close(events)

	p.turnMu.Lock()
	defer p.turnMu.Unlock()

	ts := &turnState{
		inbox:      make(chan ai.Event, 16),
		term:       make(chan struct{}),
		quit:       make(chan struct{}),
		ctx:        ctx,
		onApproval: p.cfg.OnApproval,
		toolPolicy: aitools.ResolveOptions{Preferences: req.ToolPreferences, Policy: req.ToolPolicy},
		planMode:   req.Permissions.Mode == api.PermissionPlan,
		unsettled:  map[string]bool{},
	}
	promptID := uuid.NewString()
	ts.unsettled[promptID] = true
	p.setActive(ts)
	defer func() {
		close(ts.quit)
		p.clearActive()
	}()

	if err := ai.ValidateAttachmentCompatibility([]api.Model{{Name: p.model, Provider: api.Anthropic, Mode: api.ModeAgent}}, req.Prompt.Attachments); err != nil {
		emit(ctx, events, ai.Event{Kind: ai.EventError, Error: err.Error(), Model: p.model})
		return
	}
	params, err := buildPromptParams(req, promptID)
	if err != nil {
		emit(ctx, events, ai.Event{Kind: ai.EventError, Error: err.Error(), Model: p.model})
		return
	}
	if _, err := p.rpc.Call(ctx, methodPrompt, params); err != nil {
		emit(ctx, events, ai.Event{Kind: ai.EventError, Error: fmt.Sprintf("claude-agent prompt failed: %v", err), Model: p.model})
		return
	}

	for {
		select {
		case ev := <-ts.inbox:
			if !emit(ctx, events, p.withCapturedOutput(ev)) {
				return
			}
		case <-ts.term:
			p.drainInbox(ctx, ts.inbox, events)
			return
		case <-ctx.Done():
			p.interrupt()
			emit(context.Background(), events, ai.Event{Kind: ai.EventError, Error: "claude-agent: context cancelled", Model: p.model})
			return
		case <-p.baseCtx.Done():
			emit(context.Background(), events, ai.Event{Kind: ai.EventError, Error: "claude-agent: provider closed", Model: p.model})
			return
		case <-p.procExited:
			emit(context.Background(), events, ai.Event{Kind: ai.EventError, Error: p.withProcessOutput("claude-agent: process exited mid-turn"), Model: p.model})
			return
		}
	}
}

// onNotification routes a server notification to the active turn. It runs on the
// jsonrpc read loop goroutine, so it must not block indefinitely: sends select
// on the turn's quit and the provider's base context.
func (p *Provider) onNotification(method string, params json.RawMessage) {
	ev, ok := mapNotification(method, params, p.model)
	if ok && ev.SessionID != "" {
		p.rememberSession(ev.SessionID)
	}

	p.activeMu.Lock()
	ts := p.active
	p.activeMu.Unlock()
	if ts == nil {
		return
	}

	switch method {
	case notifyTurnDone:
		// A settled turn is not the end of the captain turn: background agents
		// may still be running and wake a follow-up turn. Hold the result until
		// the session goes idle.
		consumed, err := consumedPrompts(params)
		if err != nil {
			p.deliver(ts, ai.Event{Kind: ai.EventError, Error: err.Error(), Model: p.model})
			ts.abort()
			return
		}
		ts.settled(ev, ok && !ts.interrupting.Load(), consumed)
		return
	case notifySessionState:
		if sessionState(params) != sessionIdle {
			return
		}
		if result, done := ts.idle(); done {
			p.endTurn(ts, result)
		}
		return
	case notifyTurnError:
		// The SDK query loop died: no prompt it held will settle.
		if ok && !ts.interrupting.Load() {
			p.deliver(ts, ev)
		}
		ts.abort()
		return
	}
	if ok {
		p.deliver(ts, ev)
	}
}

// endTurn delivers the turn's held result, if any, then drains it.
func (p *Provider) endTurn(ts *turnState, result *ai.Event) {
	if result != nil {
		p.deliver(ts, *result)
	}
	close(ts.term)
}

// consumedPrompts reads the uuids of the prompts an SDK result consumed. A
// result names every prompt it folded in (user_message_uuids); an error result
// may carry only the one that triggered it (user_message_uuid). A follow-up
// turn a background agent woke consumed none.
func consumedPrompts(params json.RawMessage) ([]string, error) {
	var result struct {
		UUIDs []string `json:"user_message_uuids"`
		UUID  string   `json:"user_message_uuid"`
	}
	if err := json.Unmarshal(params, &result); err != nil {
		return nil, fmt.Errorf("claude-agent: decode the prompts a turn/completed consumed: %w", err)
	}
	if result.UUID != "" && !slices.Contains(result.UUIDs, result.UUID) {
		result.UUIDs = append(result.UUIDs, result.UUID)
	}
	return result.UUIDs, nil
}

// deliver hands ev to the turn without blocking past the turn's or the
// provider's lifetime.
func (p *Provider) deliver(ts *turnState, ev ai.Event) {
	select {
	case ts.inbox <- ev:
	case <-ts.quit:
	case <-p.baseCtx.Done():
	}
}

// withCapturedOutput attaches the child's captured stdio to an error event.
//
// It runs on the turn's own goroutine, never the jsonrpc read loop: the wait in
// withProcessOutput ends when the child's pumps drain, and draining stdout is
// the read loop's own job — enriching there would make the wait outlive its
// cause.
func (p *Provider) withCapturedOutput(ev ai.Event) ai.Event {
	if ev.Kind == ai.EventError {
		ev.Error = p.withProcessOutput(ev.Error)
	}
	return ev
}

// withProcessOutput appends the child's captured stdio to a terminal
// diagnostic.
//
// stdout and stderr are pumped independently, so the turn/error announcing a
// subprocess failure arrives before the stderr flush that explains it: the
// message then carries the JSON-RPC transcript and none of the subprocess's own
// output — an authentication detail written to stderr goes missing from the very
// error raised to report it. awaitStderr reconciles the two pipes first.
func (p *Provider) withProcessOutput(message string) string {
	p.procMu.RLock()
	proc := p.proc
	p.procMu.RUnlock()
	if proc == nil {
		return message
	}
	p.awaitStderr(proc)

	stdout := outputTail(proc.GetStdout())
	stderr := outputTail(proc.GetStderr())
	if stdout == "" && stderr == "" {
		return message
	}

	var detail strings.Builder
	detail.WriteString(message)
	if stdout != "" {
		detail.WriteString("\nstdout (JSON-RPC):\n")
		detail.WriteString(stdout)
	}
	if stderr != "" {
		detail.WriteString("\nstderr:\n")
		detail.WriteString(stderr)
	}
	return detail.String()
}

// awaitStderr gives the child's stderr pump a bounded chance to land before its
// output is read. It returns the moment stderr has anything, or the child is
// gone and no more is coming — a diagnostic never waits on a pipe that already
// spoke.
func (p *Provider) awaitStderr(proc *exec.Process) {
	deadline := time.Now().Add(processOutputDrain)
	for proc.GetStderr() == "" && time.Now().Before(deadline) {
		select {
		case <-p.procExited:
			return
		case <-p.baseCtx.Done():
			return
		case <-time.After(processOutputPoll):
		}
	}
}

func outputTail(output string) string {
	output = strings.TrimSpace(output)
	if len(output) <= errorOutputLimit {
		return output
	}
	return "[truncated to last 16 KiB]\n" + output[len(output)-errorOutputLimit:]
}

// onRequest answers a server→client request from agent.ts. It runs on its own
// goroutine (per jsonrpc.Handlers.OnRequest), so blocking on a human approval is
// safe. Unknown methods get method-not-found.
func (p *Provider) onRequest(req jsonrpc.ServerRequest) (any, *jsonrpc.RPCError) {
	switch req.Method {
	case methodCanUseTool:
		return p.handleCanUseTool(req.Params)
	case methodElicit:
		return p.handleElicit(req.Params)
	}
	return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found: " + req.Method}
}

// activeTurn is the turn in flight, or nil between turns.
func (p *Provider) activeTurn() *turnState {
	p.activeMu.Lock()
	defer p.activeMu.Unlock()
	return p.active
}

func (p *Provider) currentSessionID() string {
	p.sessMu.Lock()
	defer p.sessMu.Unlock()
	return p.sessionID
}

func (p *Provider) Steer(ctx context.Context, req api.Spec) error {
	p.activeMu.Lock()
	ts := p.active
	p.activeMu.Unlock()
	promptID := uuid.NewString()
	if ts == nil || !ts.addPrompt(promptID) {
		return fmt.Errorf("claude-agent: no active turn to steer")
	}
	params, err := buildPromptParams(req, promptID)
	if err == nil {
		_, err = p.rpc.Call(ctx, methodPrompt, params)
		if err != nil {
			err = fmt.Errorf("claude-agent steer failed: %w", err)
		}
	}
	if err != nil {
		if result, done := ts.withdraw(promptID); done {
			p.endTurn(ts, result)
		}
	}
	return err
}

func (p *Provider) Interrupt(ctx context.Context) error {
	if p.rpc == nil {
		return fmt.Errorf("claude-agent: provider not started")
	}
	p.activeMu.Lock()
	ts := p.active
	p.activeMu.Unlock()
	if ts == nil {
		return fmt.Errorf("claude-agent: no active turn to interrupt")
	}
	ts.interrupting.Store(true)
	if _, err := p.rpc.Call(ctx, methodInterrupt, nil); err != nil {
		ts.interrupting.Store(false)
		return fmt.Errorf("claude-agent interrupt failed: %w", err)
	}
	return nil
}

// SetPermissionMode switches the live SDK session's posture; the SDK applies it
// to the in-flight turn as well as later ones. It refuses the same
// bypass-while-brokered combination initialize refuses.
func (p *Provider) SetPermissionMode(ctx context.Context, mode api.PermissionMode) error {
	runtime := api.RuntimeOf(api.Anthropic, api.ModeAgent)
	if mode == "" || !api.PermissionCapabilitiesFor(runtime).ModeSupport(mode).Honoured() {
		return fmt.Errorf("permissions.mode %q is not supported by %s", mode, runtime)
	}
	if p.cfg.OnApproval != nil && mode == api.PermissionBypass {
		return fmt.Errorf("claude-agent: bypassPermissions cannot bypass brokered tool approvals")
	}
	select {
	case <-p.initDone:
		if p.initErr != nil {
			return fmt.Errorf("claude-agent: provider not started: %w", p.initErr)
		}
	default:
		return fmt.Errorf("claude-agent: provider not started")
	}
	if _, err := p.rpc.Call(ctx, methodSetPermissionMode, map[string]string{"mode": string(mode)}); err != nil {
		return fmt.Errorf("claude-agent set_permission_mode failed: %w", err)
	}
	return nil
}

func (p *Provider) interrupt() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = p.Interrupt(ctx)
}

func (p *Provider) setActive(ts *turnState) {
	p.activeMu.Lock()
	p.active = ts
	p.activeMu.Unlock()
}

func (p *Provider) clearActive() {
	p.activeMu.Lock()
	p.active = nil
	p.activeMu.Unlock()
}

func (p *Provider) rememberSession(id string) {
	p.sessMu.Lock()
	p.sessionID = id
	p.sessMu.Unlock()
}

// summary projects the turn for a task snapshot. It reads the unsettled
// prompts under the same lock that maintains them, because the snapshot path
// runs concurrently with the turn it is describing.
func (ts *turnState) summary() *ai.AgentTurn {
	ts.promptMu.Lock()
	pending := len(ts.unsettled)
	ts.promptMu.Unlock()
	return &ai.AgentTurn{
		PlanMode:     ts.planMode,
		Pending:      pending,
		Interrupting: ts.interrupting.Load(),
	}
}

// addPrompt holds the turn open for a steered prompt until a result names it.
// An idle seen before it says nothing about it, so the turn awaits a new one.
func (ts *turnState) addPrompt(promptID string) bool {
	ts.promptMu.Lock()
	defer ts.promptMu.Unlock()
	if ts.ended {
		return false
	}
	ts.unsettled[promptID] = true
	ts.idleWaiting = false
	return true
}

// settled records one SDK result: the prompts it consumed are settled, and
// when keep is set its event folds into the result held for the captain turn.
func (ts *turnState) settled(ev ai.Event, keep bool, consumed []string) {
	ts.promptMu.Lock()
	defer ts.promptMu.Unlock()
	if ts.ended {
		return
	}
	for _, promptID := range consumed {
		delete(ts.unsettled, promptID)
	}
	if keep {
		merged := mergeResult(ts.result, ev)
		ts.result = &merged
	}
}

// idle ends the turn once every prompt it sent has settled, returning the held
// result to deliver (nil when an interrupt dropped it). The caller closes term
// after delivering it, so the result is queued before the turn drains.
func (ts *turnState) idle() (*ai.Event, bool) {
	ts.promptMu.Lock()
	defer ts.promptMu.Unlock()
	if ts.ended {
		return nil, false
	}
	if len(ts.unsettled) > 0 {
		ts.idleWaiting = true
		return nil, false
	}
	return ts.endLocked()
}

// withdraw forgets a steered prompt the bridge never accepted. When an idle
// was only waiting on it, the turn ends now: no further idle is coming.
func (ts *turnState) withdraw(promptID string) (*ai.Event, bool) {
	ts.promptMu.Lock()
	defer ts.promptMu.Unlock()
	delete(ts.unsettled, promptID)
	if ts.ended || len(ts.unsettled) > 0 || !ts.idleWaiting {
		return nil, false
	}
	return ts.endLocked()
}

func (ts *turnState) endLocked() (*ai.Event, bool) {
	ts.ended = true
	if ts.interrupting.Load() {
		return nil, true
	}
	return ts.result, true
}

// abort ends the turn at once: the SDK query loop died, so no prompt settles.
func (ts *turnState) abort() {
	ts.promptMu.Lock()
	defer ts.promptMu.Unlock()
	if ts.ended {
		return
	}
	ts.ended = true
	close(ts.term)
}

// emit sends ev on events, honouring ctx cancellation. Returns false if ctx was
// cancelled before the send completed.
func emit(ctx context.Context, events chan ai.Event, ev ai.Event) bool {
	select {
	case events <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// drainInbox flushes any events the handler already queued (e.g. the terminal
// result enqueued just before term closed) without blocking.
func (p *Provider) drainInbox(ctx context.Context, inbox chan ai.Event, events chan ai.Event) {
	for {
		select {
		case ev := <-inbox:
			if !emit(ctx, events, p.withCapturedOutput(ev)) {
				return
			}
		default:
			return
		}
	}
}
