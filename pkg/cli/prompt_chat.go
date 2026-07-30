package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
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
	terminal        bool
	startedAt       time.Time
	binding         *promptSessionBinding
}

func newChatSession(runID string, rendered PromptRenderResult, timeout time.Duration, stream *runStream, binding *promptSessionBinding) *chatSession {
	capabilities := chatCapabilitiesForBackend(rendered.Backend)
	chat := &chatSession{
		runID: runID, rendered: rendered, timeout: timeout, stream: stream, binding: binding,
		wake: make(chan struct{}, 1), startedAt: time.Now(),
		state: ChatStateFrame{
			RunID: runID, Status: "starting", Capabilities: capabilities,
		},
	}
	stream.setChatState(chat.state)
	return chat
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
		return c.fail(t, fmt.Errorf("backend %s does not support streaming", c.rendered.Backend))
	}
	c.mu.Lock()
	c.provider = provider
	c.streamer = streamer
	c.acc = newPromptEventAccumulator(c.stream.publish, t, c.rendered.Model, c.rendered.Backend)
	c.acc.cwd = req.Cwd()
	c.acc.idPrefix = c.runID
	c.mu.Unlock()

	next := req
	for {
		summary, interrupted, turnErr := c.runTurn(baseCtx, t, next)
		if turnErr != nil {
			if c.stream.wasStopped() {
				turnErr = errors.New("stopped")
			}
			return c.fail(t, turnErr)
		}
		if c.stream.wasStopped() {
			return c.fail(t, errors.New("stopped"))
		}
		if !interrupted {
			c.persistTurn(next, summary)
		}
		if !summary.Success && !interrupted {
			return c.fail(t, errors.New(summary.Error))
		}
		nextMessage, waitErr := c.waitForMessage(baseCtx, summary)
		if errors.Is(waitErr, errChatIdle) {
			return c.complete(t, summary), nil
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

func (c *chatSession) runTurn(baseCtx context.Context, t *task.Task, req ai.Request) (PromptRunSummary, bool, error) {
	turnCtx, cancel := runContext(baseCtx, req, c.timeout)
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
		return PromptRunSummary{}, false, err
	}
	started := false
	var eventErr string
	for event := range events {
		if !started {
			started = true
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
	interrupted := c.interruptedTurn == turn
	if interrupted {
		c.interruptedTurn = 0
	}
	c.mu.Unlock()
	if eventErr != "" && !interrupted {
		return PromptRunSummary{}, false, errors.New(eventErr)
	}
	summary := PromptRunSummary{
		RunID: c.runID, SessionID: sessionID, Model: model,
		Backend: string(c.provider.GetBackend()), InputTokens: usage.InputTokens,
		OutputTokens: usage.OutputTokens, CostUSD: cost,
		Duration: time.Since(c.startedAt).Round(time.Millisecond).String(), Success: true,
	}
	return summary, interrupted, nil
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
	c.state.Capabilities = chatCapabilitiesForBackend(string(c.provider.GetBackend()))
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

func (c *chatSession) persistTurn(req ai.Request, summary PromptRunSummary) {
	turn := c.state.Turn
	runID := c.runID
	if turn > 1 {
		runID = fmt.Sprintf("%s-turn-%d", c.runID, turn)
	}
	rendered := c.rendered
	rendered.Input = req
	persistPromptRun(context.Background(), promptRunRecordInput{
		Rendered: rendered, RunID: runID, SessionID: summary.SessionID,
		Binding: c.binding, Model: summary.Model, Backend: summary.Backend,
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
	if c.binding != nil {
		persistPromptRun(context.Background(), promptRunRecordInput{
			Rendered: c.rendered, RunID: c.runID, Binding: c.binding,
			Model: c.rendered.Model, Backend: c.rendered.Backend, Error: err.Error(),
		})
	}
	summary := c.stream.fail(err.Error())
	_, _ = t.FailedWithError(err)
	return summary, err
}

func (c *chatSession) followUpRequest(base ai.Request, text string) ai.Request {
	req := base
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
