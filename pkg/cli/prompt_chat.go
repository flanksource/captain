package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/clicky/task"
	"github.com/google/uuid"
)

const chatIdleTimeout = 10 * time.Minute

type chatSession struct {
	mu       sync.Mutex
	runID    string
	rendered PromptRenderResult
	timeout  time.Duration
	stream   *runStream
	wake     chan struct{}

	provider   ai.Provider
	streamer   ai.StreamingProvider
	baseCancel context.CancelFunc
	turnCancel context.CancelFunc
	turnDone   chan struct{}
	acc        *promptEventAccumulator

	state           ChatStateFrame
	queue           []ChatQueuedMessage
	discarded       []string
	interruptedTurn int
	recordedTurns   int
	terminal        bool
	startedAt       time.Time
	binding         *promptSessionBinding
	// permissionMode is the posture explicitly requested for the run — the
	// rendered one until a switch replaces it — and what follow-up turns carry.
	permissionMode api.PermissionMode
}

func newChatSession(runID string, rendered PromptRenderResult, timeout time.Duration, stream *runStream, binding *promptSessionBinding) *chatSession {
	capabilities := chatCapabilitiesFor(rendered.Provider, rendered.Mode)
	chat := &chatSession{
		runID: runID, rendered: rendered, timeout: timeout, stream: stream, binding: binding,
		wake: make(chan struct{}, 1), startedAt: time.Now(),
		permissionMode: rendered.Input.Permissions.Mode,
		state: ChatStateFrame{
			RunID: runID, Status: "starting", Capabilities: capabilities,
		},
	}
	p, _ := api.ProviderByName(rendered.Provider)
	chat.setPermissionModesLocked(api.RuntimeOf(p, api.RuntimeMode(rendered.Mode)))
	stream.setChatState(chat.state)
	return chat
}

// setPermissionModesLocked projects the runtime's honoured postures and the
// current one onto the state frame. An unstated posture reads as default only
// where the runtime honours default at all.
func (c *chatSession) setPermissionModesLocked(runtime api.Runtime) {
	c.state.PermissionModes = api.SupportedPermissionModes(providerOf(runtime), runtime.Mode)
	c.state.PermissionMode = c.permissionMode
	if c.permissionMode == "" && slices.Contains(c.state.PermissionModes, api.PermissionDefault) {
		c.state.PermissionMode = api.PermissionDefault
	}
}

func (c *chatSession) run(t *task.Task) (PromptRunSummary, error) {
	baseCtx, cancel := context.WithCancel(t.Context())
	c.mu.Lock()
	c.baseCancel = cancel
	c.mu.Unlock()
	c.stream.setCancel(cancel)
	defer cancel()

	req := c.rendered.Input
	if err := preparePromptAttachments(baseCtx, &req, c.rendered.Config); err != nil {
		return c.fail(t, err)
	}
	provider, cleanup, err := buildProvider(baseCtx, &req, c.rendered.Config)
	if err != nil {
		return c.fail(t, err)
	}
	defer cleanup()
	defer closeProvider(provider)
	streamer, ok := provider.(ai.StreamingProvider)
	if !ok {
		return c.fail(t, fmt.Errorf("runtime %s/%s does not support streaming", c.rendered.Provider, c.rendered.Mode))
	}
	c.mu.Lock()
	c.provider = provider
	c.streamer = streamer
	c.acc = newPromptEventAccumulator(c.stream.publish, t, c.rendered.Model, c.rendered.Mode)
	c.acc.cwd = req.Cwd()
	c.acc.idPrefix = c.runID
	c.mu.Unlock()

	next := req
	for {
		turn, turnErr := c.runTurn(baseCtx, next)
		if err := c.endTurn(next, turn, turnErr); err != nil {
			return c.fail(t, err)
		}
		nextMessage, waitErr := c.waitForMessage(baseCtx, turn.Summary)
		if errors.Is(waitErr, errChatIdle) {
			return c.complete(t, turn.Summary), nil
		}
		if waitErr != nil {
			if c.stream.wasStopped() || errors.Is(waitErr, context.Canceled) {
				return c.fail(t, errors.New("stopped"))
			}
			return c.fail(t, waitErr)
		}
		next = c.followUpRequest(req, nextMessage.Text)
	}
}

var errChatIdle = errors.New("chat idle timeout")

// chatTurn is how one turn of a chat ended.
type chatTurn struct {
	Summary     PromptRunSummary
	Interrupted bool
	// Delivered is set once the provider answered the turn with any event: its
	// prompt reached the provider, so the turn is part of the session's history.
	Delivered bool
}

// endTurn records a turn that reached the provider, however it ended, and
// returns the error that ends the chat, if any.
func (c *chatSession) endTurn(req ai.Request, turn chatTurn, turnErr error) error {
	if c.stream.wasStopped() {
		turnErr = errors.New("stopped")
	}
	if turn.Delivered {
		c.persistTurn(req, turn, turnErr)
	}
	return turnErr
}

func (c *chatSession) runTurn(baseCtx context.Context, req ai.Request) (chatTurn, error) {
	turnCtx, cancel, err := runContext(baseCtx, req, c.timeout)
	if err != nil {
		return chatTurn{}, err
	}
	turnDone := make(chan struct{})
	c.mu.Lock()
	c.state.Turn++
	turn := c.state.Turn
	c.state.Status = "starting"
	c.turnCancel = cancel
	c.turnDone = turnDone
	state := c.stateCopyLocked()
	streamer := c.streamer
	c.mu.Unlock()
	c.stream.setChatState(state)
	defer func() {
		cancel()
		close(turnDone)
		c.mu.Lock()
		c.turnCancel = nil
		c.turnDone = nil
		c.mu.Unlock()
	}()

	events, err := streamer.ExecuteStream(turnCtx, req)
	if err != nil {
		return chatTurn{}, err
	}
	var result chatTurn
	var eventErr string
	for event := range events {
		if !result.Delivered {
			result.Delivered = true
			c.markRunning(event.SessionID)
		}
		if event.SessionID != "" {
			c.rememberSession(event.SessionID)
		}
		if event.Kind == ai.EventError {
			eventErr = event.Error
		}
		c.acc.handle(turn, event)
	}
	sessionID, model, usage, cost := c.acc.snapshot()
	if sessionID != "" {
		c.rememberSession(sessionID)
	}
	c.mu.Lock()
	result.Interrupted = c.interruptedTurn == turn
	if result.Interrupted {
		c.interruptedTurn = 0
	}
	c.mu.Unlock()
	result.Summary = PromptRunSummary{
		RunID: c.runID, SessionID: sessionID, Model: model,
		Provider: c.provider.GetRuntime().Provider, Mode: string(c.provider.GetRuntime().Mode), InputTokens: usage.InputTokens,
		OutputTokens: usage.OutputTokens, CostUSD: cost,
		Duration: time.Since(c.startedAt).Round(time.Millisecond).String(), Success: true,
	}
	if eventErr != "" && !result.Interrupted {
		result.Summary.Success, result.Summary.Error = false, eventErr
		return result, errors.New(eventErr)
	}
	return result, nil
}

func (c *chatSession) waitForMessage(ctx context.Context, summary PromptRunSummary) (ChatQueuedMessage, error) {
	c.mu.Lock()
	if len(c.queue) > 0 {
		message := c.popQueueLocked()
		state := c.stateCopyLocked()
		c.mu.Unlock()
		c.stream.setChatState(state)
		return message, nil
	}
	if !c.state.Capabilities.FollowUp {
		c.mu.Unlock()
		return ChatQueuedMessage{}, errChatIdle
	}
	c.state.Status = "idle"
	c.state.Summary = &summary
	state := c.stateCopyLocked()
	c.mu.Unlock()
	c.stream.setChatState(state)

	timer := time.NewTimer(chatIdleTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ChatQueuedMessage{}, ctx.Err()
	case <-timer.C:
		return ChatQueuedMessage{}, errChatIdle
	case <-c.wake:
		c.mu.Lock()
		if len(c.queue) == 0 {
			c.mu.Unlock()
			return ChatQueuedMessage{}, fmt.Errorf("chat wake without a queued message")
		}
		message := c.popQueueLocked()
		state := c.stateCopyLocked()
		c.mu.Unlock()
		c.stream.setChatState(state)
		return message, nil
	}
}

func (c *chatSession) send(ctx context.Context, request ChatMessageRequest) (ChatMessageResponse, error) {
	text := strings.TrimSpace(request.Text)
	if text == "" {
		return ChatMessageResponse{}, newChatError(http.StatusBadRequest, "message text is required")
	}
	messageID := strings.TrimSpace(request.MessageID)
	if messageID == "" {
		messageID = uuid.NewString()
	}
	message := ChatQueuedMessage{MessageID: messageID, Text: text}
	if request.PermissionMode != "" && request.PermissionMode != c.currentPermissionMode() {
		if _, err := c.setPermissionMode(ctx, request.PermissionMode); err != nil {
			return ChatMessageResponse{}, err
		}
	}

	c.mu.Lock()
	if c.terminal {
		c.mu.Unlock()
		return ChatMessageResponse{}, newChatError(http.StatusConflict, "run is terminal")
	}
	status := c.state.Status
	capabilities := c.state.Capabilities
	provider := c.provider
	if status == "starting" || status == "interrupting" || status == "stopping" {
		c.mu.Unlock()
		return ChatMessageResponse{}, newChatError(http.StatusConflict, "run is not ready for a message")
	}
	if status == "running" && capabilities.Steer {
		c.mu.Unlock()
		steerer, ok := api.ProviderAs[api.SteerableProvider](provider)
		if !ok {
			return ChatMessageResponse{}, newChatError(http.StatusConflict, "active provider cannot be steered")
		}
		req := c.followUpRequest(c.rendered.Input, text)
		if err := steerer.Steer(ctx, req); err != nil {
			return ChatMessageResponse{}, err
		}
		c.publishUser(message)
		return ChatMessageResponse{RunID: c.runID, MessageID: messageID, Status: "steered", Capabilities: capabilities}, nil
	}
	if !capabilities.FollowUp {
		c.mu.Unlock()
		return ChatMessageResponse{}, newChatError(http.StatusConflict, "active provider cannot accept follow-up messages")
	}
	c.queue = append(c.queue, message)
	c.state.Queued = append([]ChatQueuedMessage(nil), c.queue...)
	state := c.stateCopyLocked()
	c.mu.Unlock()
	c.publishUser(message)
	c.stream.setChatState(state)
	if status == "idle" {
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
	responseStatus := "queued"
	if status == "idle" {
		responseStatus = "started"
	}
	return ChatMessageResponse{RunID: c.runID, MessageID: messageID, Status: responseStatus, Capabilities: capabilities}, nil
}

func (c *chatSession) interrupt(ctx context.Context) (ChatInterruptResponse, error) {
	c.mu.Lock()
	if c.terminal || c.state.Status != "running" || !c.state.Capabilities.Interrupt {
		c.mu.Unlock()
		return ChatInterruptResponse{}, newChatError(http.StatusConflict, "run cannot be interrupted")
	}
	provider := c.provider
	turn := c.state.Turn
	queued := append([]ChatQueuedMessage(nil), c.queue...)
	c.state.Status = "interrupting"
	state := c.stateCopyLocked()
	c.mu.Unlock()
	c.stream.setChatState(state)

	interruptible, ok := api.ProviderAs[api.InterruptibleProvider](provider)
	if !ok {
		c.restoreRunning()
		return ChatInterruptResponse{}, newChatError(http.StatusConflict, "active provider cannot be interrupted")
	}
	if err := interruptible.Interrupt(ctx); err != nil {
		c.restoreRunning()
		return ChatInterruptResponse{}, err
	}

	c.mu.Lock()
	c.interruptedTurn = turn
	c.queue = nil
	discarded := make([]string, 0, len(queued))
	for _, message := range queued {
		discarded = append(discarded, message.MessageID)
	}
	c.discarded = append(c.discarded, discarded...)
	c.state.Queued = nil
	c.state.DiscardedMessageIDs = append([]string(nil), c.discarded...)
	state = c.stateCopyLocked()
	turnCancel := c.turnCancel
	turnDone := c.turnDone
	c.mu.Unlock()
	c.stream.setChatState(state)
	go cancelTurnBackstop(turnDone, turnCancel)
	return ChatInterruptResponse{Status: "interrupting", DiscardedMessageIDs: discarded}, nil
}

// setPermissionMode switches the live run's posture through the provider and
// records it for the state frame and every later follow-up turn.
func (c *chatSession) setPermissionMode(ctx context.Context, mode api.PermissionMode) (ChatPermissionModeResponse, error) {
	if mode == "" || !mode.Valid() {
		return ChatPermissionModeResponse{}, newChatError(http.StatusBadRequest, fmt.Sprintf("invalid permission mode %q", mode))
	}
	c.mu.Lock()
	terminal, status, provider := c.terminal, c.state.Status, c.provider
	switchable := c.state.Capabilities.SetPermissionMode
	supported := slices.Contains(c.state.PermissionModes, mode)
	c.mu.Unlock()
	switch {
	case terminal:
		return ChatPermissionModeResponse{}, newChatError(http.StatusConflict, "run is terminal")
	case !switchable:
		return ChatPermissionModeResponse{}, newChatError(http.StatusConflict, "active runtime cannot switch permission mode")
	case !supported:
		return ChatPermissionModeResponse{}, newChatError(http.StatusUnprocessableEntity,
			fmt.Sprintf("permission mode %q is not supported by the active runtime", mode))
	case provider == nil || status == "starting" || status == "stopping":
		return ChatPermissionModeResponse{}, newChatError(http.StatusConflict, "run is not ready for a permission mode change")
	}
	switcher, ok := api.ProviderAs[api.PermissionSwitchableProvider](provider)
	if !ok {
		return ChatPermissionModeResponse{}, fmt.Errorf("runtime %s declares a permission mode switch its provider does not implement", provider.GetRuntime())
	}
	if err := switcher.SetPermissionMode(ctx, mode); err != nil {
		return ChatPermissionModeResponse{}, err
	}
	c.mu.Lock()
	c.permissionMode = mode
	c.state.PermissionMode = mode
	state := c.stateCopyLocked()
	c.mu.Unlock()
	c.stream.setChatState(state)
	return ChatPermissionModeResponse{RunID: c.runID, PermissionMode: mode}, nil
}

func (c *chatSession) currentPermissionMode() api.PermissionMode {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.PermissionMode
}

func cancelTurnBackstop(done <-chan struct{}, cancel context.CancelFunc) {
	if cancel == nil {
		return
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		cancel()
	}
}

func (c *chatSession) stop() bool {
	c.mu.Lock()
	if c.terminal {
		c.mu.Unlock()
		return false
	}
	c.state.Status = "stopping"
	state := c.stateCopyLocked()
	cancel := c.baseCancel
	c.mu.Unlock()
	c.stream.setChatState(state)
	c.stream.requestStop()
	if cancel != nil {
		cancel()
	}
	return true
}

func (c *chatSession) markRunning(sessionID string) {
	c.mu.Lock()
	c.state.Status = "running"
	c.state.Capabilities = chatCapabilitiesForRuntime(providerOf(c.provider.GetRuntime()), c.provider.GetRuntime().Mode)
	c.setPermissionModesLocked(c.provider.GetRuntime())
	if sessionID != "" {
		c.state.SessionID = sessionID
	}
	state := c.stateCopyLocked()
	c.mu.Unlock()
	c.stream.setChatState(state)
}

func (c *chatSession) rememberSession(sessionID string) {
	c.mu.Lock()
	c.state.SessionID = sessionID
	state := c.stateCopyLocked()
	c.mu.Unlock()
	c.stream.setChatState(state)
	promptChats.bindSession(c, sessionID)
}

// persistTurn records a turn under its own run: succeeded, failed with the
// error that ended it, or cancelled when the person interrupted or stopped it.
func (c *chatSession) persistTurn(req ai.Request, turn chatTurn, turnErr error) {
	c.mu.Lock()
	number := c.state.Turn
	c.recordedTurns++
	c.mu.Unlock()
	runID := c.runID
	if number > 1 {
		runID = fmt.Sprintf("%s-turn-%d", c.runID, number)
	}
	rendered := c.rendered
	rendered.Input = req
	summary := turn.Summary
	record := promptRunRecordInput{
		Rendered: rendered, RunID: runID, SessionID: firstNonEmpty(summary.SessionID, c.sessionID()),
		Binding: c.binding, Model: summary.Model, Provider: providerOf(api.Runtime{Provider: summary.Provider, Mode: api.RuntimeMode(summary.Mode)}), Mode: api.RuntimeMode(summary.Mode),
		ResultText: summary.Text, ResultJSON: summary.StructuredOutput,
	}
	if turnErr != nil {
		record.Error = turnErr.Error()
	}
	if turn.Interrupted || c.stream.wasStopped() {
		record.State = database.PromptRunStateCancelled
	}
	persistPromptRun(context.Background(), record)
}

// recordFailedRun gives a batch chat that failed before any of its turns was
// recorded its run row. A chat with a recorded turn already has its record.
func (c *chatSession) recordFailedRun(err error) {
	c.mu.Lock()
	recorded := c.recordedTurns > 0
	c.mu.Unlock()
	if c.binding == nil || recorded {
		return
	}
	persistPromptRun(context.Background(), promptRunRecordInput{
		Rendered: c.rendered, RunID: c.runID, Binding: c.binding,
		Model: c.rendered.Model, Provider: providerOf(api.Runtime{Provider: c.rendered.Provider, Mode: api.RuntimeMode(c.rendered.Mode)}), Mode: api.RuntimeMode(c.rendered.Mode), Error: err.Error(),
	})
}

func (c *chatSession) complete(t *task.Task, summary PromptRunSummary) PromptRunSummary {
	c.mu.Lock()
	c.terminal = true
	c.state.Summary = &summary
	c.mu.Unlock()
	promptChats.finish(c)
	c.stream.complete(summary)
	t.Success()
	return summary
}

func (c *chatSession) fail(t *task.Task, err error) (PromptRunSummary, error) {
	c.mu.Lock()
	c.terminal = true
	c.mu.Unlock()
	promptChats.finish(c)
	c.recordFailedRun(err)
	summary := c.stream.fail(err.Error())
	_, _ = t.FailedWithError(err)
	return summary, err
}

func (c *chatSession) followUpRequest(base ai.Request, text string) ai.Request {
	req := base
	c.mu.Lock()
	req.Permissions.Mode = c.permissionMode
	c.mu.Unlock()
	req.Prompt.User = text
	req.Prompt.Attachments = nil
	req.Prompt.Schema = nil
	req.Prompt.SchemaJSON = nil
	req.Workflow = nil
	req.Setup = nil
	return req
}

func (c *chatSession) publishUser(message ChatQueuedMessage) {
	c.stream.publish(session.Message{
		ID: message.MessageID, Role: "user",
		Parts: []session.Part{{Type: session.PartText, Text: message.Text}},
	})
}

func (c *chatSession) popQueueLocked() ChatQueuedMessage {
	message := c.queue[0]
	c.queue = c.queue[1:]
	c.state.Queued = append([]ChatQueuedMessage(nil), c.queue...)
	return message
}

func (c *chatSession) stateCopyLocked() ChatStateFrame {
	state := c.state
	state.Queued = append([]ChatQueuedMessage(nil), c.state.Queued...)
	state.DiscardedMessageIDs = append([]string(nil), c.state.DiscardedMessageIDs...)
	return state
}

func (c *chatSession) restoreRunning() {
	c.mu.Lock()
	c.state.Status = "running"
	state := c.stateCopyLocked()
	c.mu.Unlock()
	c.stream.setChatState(state)
}

func (c *chatSession) terminalState() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.terminal
}

func (c *chatSession) sessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.SessionID
}

func (c *chatSession) projection() (string, ChatCapabilities, *ChatStateFrame) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.stateCopyLocked()
	return c.runID, state.Capabilities, &state
}

func closeProvider(provider ai.Provider) {
	if closer, ok := api.ProviderAs[api.CloseableProvider](provider); ok {
		if err := closer.Close(); err != nil {
			log.Errorf("close chat provider: %v", err)
		}
	}
}

type chatHTTPError struct {
	status int
	msg    string
}

func (e chatHTTPError) Error() string { return e.msg }

func newChatError(status int, message string) error {
	return chatHTTPError{status: status, msg: message}
}
