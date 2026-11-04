// Tests the channel between a relocated run and its caller over the run's stdout and stdin: the
// caller sees each message as it is sent, answers calls by ID, and gets the run's result.
package remoterun

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/promptrun"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// channel connects an environment's Stdio to a caller's Relay, as a process's stdout and stdin do.
type channel struct {
	stdoutR *io.PipeReader
	stdoutW *io.PipeWriter
	stdinR  *io.PipeReader
	stdinW  *io.PipeWriter
}

func newChannel(t *testing.T) *channel {
	c := &channel{}
	c.stdoutR, c.stdoutW = io.Pipe()
	c.stdinR, c.stdinW = io.Pipe()
	t.Cleanup(func() {
		_ = c.stdoutW.Close()
		_ = c.stdinW.Close()
	})
	return c
}

// environment runs fn as the run in the environment, then ends its stdout, as the process exits.
func (c *channel) environment(fn func(Serve)) {
	go func() {
		fn(Stdio(c.stdoutW, c.stdinR))
		_ = c.stdoutW.Close()
	}()
}

func (c *channel) relay(t *testing.T, in promptrun.Input) (promptrun.Result, error) {
	return c.relayStarted(t, in, func(api.Spec) error { return nil })
}

func (c *channel) relayStarted(t *testing.T, in promptrun.Input, started func(api.Spec) error) (promptrun.Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return Relay(ctx, in, started, c.stdoutR, c.stdinW)
}

func TestTheCallerSeesTheRunAndAnswersItsCalls(t *testing.T) {
	c := newChannel(t)
	var events []api.Event
	var progress []api.VerifyReport
	in := promptrun.Input{OnEvent: func(iteration int, event api.Event) {
		assert.Equal(t, 1, iteration)
		events = append(events, event)
	}}
	in.Verify.Progress = func(report api.VerifyReport) { progress = append(progress, report) }
	in.Config.Tools = []api.ToolDefinition{{Name: "todo_search", Handler: func(_ context.Context, input map[string]any) (any, error) {
		return map[string]any{"found": input["query"]}, nil
	}}}
	in.Config.OnApproval = func(_ context.Context, request api.ApprovalRequest) (api.ApprovalDecision, error) {
		return api.ApprovalDecision{Allow: request.Tool == "Bash"}, nil
	}
	var tool, approval Reply
	var toolErr, approvalErr error
	c.environment(func(serve Serve) {
		_ = serve.Send(StartedMessage(api.Spec{Prompt: api.Prompt{User: "fix the build"}}))
		_ = serve.Send(EventMessage(1, api.Event{Kind: api.EventText, Text: "looking"}))
		_ = serve.Send(ProgressMessage(api.VerifyReport{Iteration: 1}))
		tool, toolErr = serve.Call(ToolCallMessage("todo_search", map[string]any{"query": "bug"}))
		approval, approvalErr = serve.Call(ApprovalMessage(api.ApprovalRequest{Tool: "Bash"}))
		_ = serve.Send(ResultMessage(promptrun.Result{Response: &api.Response{Text: "done"}, CostUSD: 0.5}, nil))
	})

	var started []api.Spec
	result, err := c.relayStarted(t, in, func(spec api.Spec) error {
		started = append(started, spec)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []api.Spec{{Prompt: api.Prompt{User: "fix the build"}}}, started)
	assert.Equal(t, "done", result.Response.Text)
	assert.Equal(t, 0.5, result.CostUSD)
	assert.Equal(t, []api.Event{{Kind: api.EventText, Text: "looking"}}, events)
	assert.Equal(t, []api.VerifyReport{{Iteration: 1}}, progress)

	require.NoError(t, toolErr)
	assert.JSONEq(t, `{"found":"bug"}`, string(tool.Output))
	require.NoError(t, approvalErr)
	require.NotNil(t, approval.Decision)
	assert.True(t, approval.Decision.Allow)
}

func TestARunsErrorReachesTheCaller(t *testing.T) {
	c := newChannel(t)
	c.environment(func(serve Serve) {
		_ = serve.Send(ResultMessage(promptrun.Result{CostUSD: 0.1}, errors.New("verification failed")))
	})
	result, err := c.relay(t, promptrun.Input{})
	require.EqualError(t, err, "verification failed")
	assert.Equal(t, 0.1, result.CostUSD, "the result comes with the run's error")
}

func TestARunWhoseStartCannotBeRecordedEndsTheRelay(t *testing.T) {
	c := newChannel(t)
	c.environment(func(serve Serve) {
		_ = serve.Send(StartedMessage(api.Spec{}))
		_ = serve.Send(ResultMessage(promptrun.Result{}, nil))
	})
	_, err := c.relayStarted(t, promptrun.Input{}, func(api.Spec) error { return errBoom })
	require.EqualError(t, err, "the relocated run started, but its start couldn't be recorded: boom")
}

func TestARunThatEndsWithoutAResultIsAnError(t *testing.T) {
	c := newChannel(t)
	c.environment(func(serve Serve) {
		_ = serve.Send(EventMessage(1, api.Event{Kind: api.EventText, Text: "working"}))
	})
	_, err := c.relay(t, promptrun.Input{})
	require.ErrorIs(t, err, ErrNoResult)
	require.EqualError(t, err, "the relocated run ended without a result")
}

func TestTheCallerRefusesACallItCannotAnswer(t *testing.T) {
	c := newChannel(t)
	var tool, approval Reply
	c.environment(func(serve Serve) {
		tool, _ = serve.Call(ToolCallMessage("todo_search", nil))
		approval, _ = serve.Call(ApprovalMessage(api.ApprovalRequest{Tool: "Bash"}))
		_ = serve.Send(ResultMessage(promptrun.Result{}, nil))
	})
	_, err := c.relay(t, promptrun.Input{})
	require.NoError(t, err)
	assert.Equal(t, `the caller has no tool "todo_search"`, tool.Error)
	assert.Equal(t, "the caller asks no approvals", approval.Error)
}

func TestACallFailsWhenTheCallerStopsAnswering(t *testing.T) {
	c := newChannel(t)
	serve := Stdio(io.Discard, c.stdinR)
	require.NoError(t, c.stdinW.Close())
	_, err := serve.Call(ToolCallMessage("todo_search", nil))
	require.EqualError(t, err, "the caller stopped answering: EOF")
}

func TestTheCallerRefusesOutputThatIsNotAMessage(t *testing.T) {
	c := newChannel(t)
	go func() {
		_, _ = io.WriteString(c.stdoutW, "Downloading dependencies...\n")
		_ = c.stdoutW.Close()
	}()
	_, err := c.relay(t, promptrun.Input{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read the relocated run's messages: ")
}

func TestRepliesCarryTheirCallsID(t *testing.T) {
	message := roundTrip(t, Message{Kind: KindTool, ID: "call-7", Tool: "todo_search"})
	assert.Equal(t, "call-7", message.ID)
	reply := roundTrip(t, Reply{ID: "call-7", Output: json.RawMessage(`1`)})
	assert.Equal(t, "call-7", reply.ID)
}

func TestTheRunsTranscriptsReachTheCaller(t *testing.T) {
	c := newChannel(t)
	c.environment(func(serve Serve) {
		_ = serve.Send(TranscriptMessage("claude", "-tmp-captain-run-src/session-1.jsonl", []byte(`{"type":"user"}`+"\n")))
		_ = serve.Send(ResultMessage(promptrun.Result{}, nil))
	})
	type file struct{ source, name, data string }
	var got []file
	in := promptrun.Input{OnTranscript: func(source, name string, data []byte) {
		got = append(got, file{source, name, string(data)})
	}}
	_, err := c.relay(t, in)
	require.NoError(t, err)
	assert.Equal(t, []file{{"claude", "-tmp-captain-run-src/session-1.jsonl", `{"type":"user"}` + "\n"}}, got)
}
