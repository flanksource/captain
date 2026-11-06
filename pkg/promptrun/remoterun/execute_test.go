// Tests Execute, the environment's side of a relocated run: it runs the job's prompt run in the
// checkout with the host's hooks, and reports the start, every event and its calls to the caller.
package remoterun

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scriptedAgent streams one scripted turn in place of an agent CLI.
type scriptedAgent struct {
	mu       sync.Mutex
	events   []ai.Event
	requests []api.Spec
}

func (a *scriptedAgent) GetModel() string        { return "claude-sonnet-5" }
func (a *scriptedAgent) GetRuntime() api.Runtime { return api.RuntimeOf(api.Anthropic, api.ModeAgent) }
func (a *scriptedAgent) Execute(context.Context, api.Spec) (*api.Response, error) {
	return nil, errors.New("the scripted agent only streams")
}
func (a *scriptedAgent) ExecuteStream(_ context.Context, req api.Spec) (<-chan ai.Event, error) {
	a.mu.Lock()
	a.requests = append(a.requests, req)
	a.mu.Unlock()
	ch := make(chan ai.Event, len(a.events))
	for _, ev := range a.events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func scriptedJob(root string) Job {
	spec := api.Spec{Model: api.Model{Name: "claude-sonnet-5", Provider: api.Anthropic, Mode: api.ModeAgent}, Prompt: api.Prompt{User: "fix the build"}}
	spec.SetCwd(root)
	return Job{Resolved: api.ResolvedSpec{Spec: spec}, Config: JobConfig{Model: spec.Model}, Timeout: time.Minute, Root: root}
}

// envHook sets a variable on the run's setup, as a host's hook does.
type envHook struct{}

func (envHook) Name() string { return "host-env" }
func (envHook) PreRun(hc *agent.HookContext) error {
	hc.Request.Setup.Env = append(hc.Request.Setup.Env, "HOST_ISSUE=todo-1")
	return nil
}

func TestExecuteReportsTheRunInOrder(t *testing.T) {
	dir := t.TempDir()
	scripted := &scriptedAgent{events: []ai.Event{
		{Kind: ai.EventSystem, SessionID: "provider-session"},
		{Kind: ai.EventText, Text: "done"},
		{Kind: ai.EventResult, Success: true, CostUSD: 0.25, Usage: &api.Usage{InputTokens: 7, OutputTokens: 3}},
	}}
	var sent []Message
	finished, err := Execute(context.Background(), scriptedJob("/home/dev/repo"), dir, Host{Provider: scripted, Hooks: []any{envHook{}}}, Serve{Send: func(message Message) error {
		sent = append(sent, message)
		return nil
	}})
	require.NoError(t, err)

	require.NotEmpty(t, sent)
	assert.Equal(t, KindStarted, sent[0].Kind)
	require.NotNil(t, sent[0].Spec)
	assert.Equal(t, dir, sent[0].Spec.Cwd(), "the run works in the environment's checkout")
	assert.Contains(t, sent[0].Spec.Setup.Env, "HOST_ISSUE=todo-1", "the host's hooks ran before the start")

	require.NoError(t, finished.Err)
	assert.Equal(t, "done", finished.Result.Response.Text, "the result is the caller's to report")
	assert.Equal(t, 0.25, finished.Result.CostUSD)

	var kinds []ai.EventKind
	for _, message := range sent[1:] {
		require.Equal(t, KindEvent, message.Kind)
		kinds = append(kinds, message.DecodedEvent().Kind)
	}
	assert.Contains(t, kinds, ai.EventSystem)
	assert.Contains(t, kinds, ai.EventText)
	require.Len(t, scripted.requests, 1)
	assert.Equal(t, "fix the build", scripted.requests[0].Prompt.User)
}

func TestExecuteStopsWhenTheCallerStopsListening(t *testing.T) {
	scripted := &scriptedAgent{events: []ai.Event{{Kind: ai.EventText, Text: "done"}, {Kind: ai.EventResult, Success: true}}}
	_, err := Execute(context.Background(), scriptedJob("/home/dev/repo"), t.TempDir(), Host{Provider: scripted}, Serve{Send: func(Message) error {
		return errors.New("broken pipe")
	}})
	require.EqualError(t, err, "report the run's start to its caller: broken pipe")
}

func TestAJobsToolsAndApprovalsCallTheCaller(t *testing.T) {
	job := scriptedJob("/home/dev/repo")
	job.Tools = []api.ToolDefinition{{Name: "todo_search", Description: "Search the backlog.", DefaultPermission: api.ToolPolicyAllow}}
	job.Approvals = true
	var calls []Message
	serve := Serve{
		Send: func(Message) error { return nil },
		Call: func(message Message) (Reply, error) {
			calls = append(calls, message)
			if message.Kind == KindTool {
				return Reply{Output: json.RawMessage(`{"found":"bug"}`)}, nil
			}
			return Reply{Decision: &api.ApprovalDecision{Allow: true}}, nil
		},
	}
	in := job.Input(t.TempDir())
	callCaller(&in, job, serve, nil)

	require.Len(t, in.Config.Tools, 1)
	assert.Equal(t, "Search the backlog.", in.Config.Tools[0].Description)
	output, err := in.Config.Tools[0].Handler(context.Background(), map[string]any{"query": "bug"})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"found": "bug"}, output)

	require.NotNil(t, in.Config.OnApproval)
	decision, err := in.Config.OnApproval(context.Background(), api.ApprovalRequest{Tool: "triage_review", Kind: api.ApprovalKindTool})
	require.NoError(t, err)
	assert.True(t, decision.Allow)

	require.Len(t, calls, 2)
	assert.Equal(t, ToolCallMessage("todo_search", map[string]any{"query": "bug"}), calls[0])
	assert.Equal(t, KindApproval, calls[1].Kind)

	refused := Serve{Call: func(Message) (Reply, error) { return Reply{Error: `the run has no tool "todo_search"`}, nil }}
	in = job.Input(t.TempDir())
	callCaller(&in, job, refused, nil)
	_, err = in.Config.Tools[0].Handler(context.Background(), nil)
	require.EqualError(t, err, `tool todo_search: the run has no tool "todo_search"`)
}

func TestAJobRunsTheHostsToolsInPlaceOfTheCallers(t *testing.T) {
	job := scriptedJob("/home/dev/repo")
	job.Tools = []api.ToolDefinition{
		{Name: "todo_search", Description: "Search the backlog."},
		{Name: "pr_thread_addressed", Description: "Report a thread addressed."},
	}
	serve := Serve{Call: func(Message) (Reply, error) {
		return Reply{Output: json.RawMessage(`"from the caller"`)}, nil
	}}
	local := []api.ToolDefinition{
		{Name: "pr_thread_addressed", Description: "Report a thread addressed.", Handler: func(context.Context, map[string]any) (any, error) { return "in the environment", nil }},
		{Name: "undeclared", Handler: func(context.Context, map[string]any) (any, error) { return nil, nil }},
	}
	in := job.Input(t.TempDir())
	callCaller(&in, job, serve, local)

	require.Len(t, in.Config.Tools, 2, "a host tool the run did not declare is not added")
	output, err := in.Config.Tools[0].Handler(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, "from the caller", output)
	output, err = in.Config.Tools[1].Handler(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, "in the environment", output)
}
