// Tests the remote run wire format: a job carries a prompt run to the environment it relocates
// to, and messages carry its progress, calls and result back, all as JSON.
package remoterun

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/ai/agent/verify"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/promptrun"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errBoom = errors.New("boom")

func roundTrip[T any](t *testing.T, value T) T {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	var out T
	require.NoError(t, json.Unmarshal(data, &out))
	return out
}

func TestAJobRebuildsThePromptRunInTheEnvironmentsCheckout(t *testing.T) {
	spec := api.Spec{Model: api.Model{Name: "claude-sonnet-5", Provider: api.Anthropic, Mode: api.ModeAgent}, Prompt: api.Prompt{User: "fix the build"}}
	spec.SetCwd("/home/dev/repo/svc")
	in := promptrun.Input{
		Resolved:          api.ResolvedSpec{Spec: spec},
		Config:            ai.Config{Model: spec.Model, NoCache: true, SessionID: "sess-1", APIKey: "never-sent"},
		CallerOwnsCommits: true,
		Verify:            verify.Options{Timeout: 5 * time.Minute},
		Timeout:           30 * time.Minute,
		Repo:              "/home/dev/repo",
	}
	in.Config.Tools = []api.ToolDefinition{{Name: "todo_search", Description: "Search the backlog.", Handler: func(context.Context, map[string]any) (any, error) { return nil, nil }}}
	in.Config.OnApproval = func(context.Context, api.ApprovalRequest) (api.ApprovalDecision, error) {
		return api.ApprovalDecision{}, nil
	}

	job := roundTrip(t, JobFor(in, json.RawMessage(`{"issue":"todo-1"}`)))
	encoded, err := json.Marshal(JobFor(in, nil))
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "never-sent", "credentials never travel in a job")
	assert.JSONEq(t, `{"issue":"todo-1"}`, string(job.Host), "the host's data travels as it is")
	require.Len(t, job.Tools, 1)
	assert.Equal(t, "Search the backlog.", job.Tools[0].Description)
	assert.Nil(t, job.Tools[0].Handler, "a tool's handler stays with the caller")
	assert.True(t, job.Approvals)

	relocated := job.Input("/workspace/src")
	assert.Equal(t, "/workspace/src/svc", relocated.Resolved.Spec.Cwd(), "the run's directory, under the environment's checkout")
	assert.Equal(t, "/workspace/src", relocated.Repo)
	assert.Equal(t, "fix the build", relocated.Resolved.Spec.Prompt.User)
	assert.Equal(t, "sess-1", relocated.Config.SessionID)
	assert.True(t, relocated.Config.NoCache)
	assert.Empty(t, relocated.Config.APIKey)
	assert.True(t, relocated.CallerOwnsCommits)
	assert.Equal(t, 5*time.Minute, relocated.Verify.Timeout)
	assert.Equal(t, 30*time.Minute, relocated.Timeout)
	assert.Nil(t, relocated.Record, "the environment records nothing")
	assert.Empty(t, relocated.Config.Tools, "Execute gives the run its tools, which call the caller")
}

func TestAJobAsksApprovalsOnlyWhenTheCallerAnswersThem(t *testing.T) {
	in := promptrun.Input{Approvals: &promptrun.ApprovalOptions{}}
	assert.False(t, JobFor(in, nil).Approvals, "a run is handed over once its approvals are bound to Config.OnApproval")
}

func TestAPathOutsideTheJobsRootMovesToTheCheckout(t *testing.T) {
	spec := api.Spec{}
	spec.SetCwd("/elsewhere")
	job := Job{Resolved: api.ResolvedSpec{Spec: spec}, Root: "/home/dev/repo"}
	assert.Equal(t, "/workspace/src", job.Input("/workspace/src").Resolved.Spec.Cwd())
}

func TestMessagesCrossAsJSONWithoutBackendNativeValues(t *testing.T) {
	event := api.Event{Kind: api.EventToolUse, Tool: "Bash", Input: map[string]any{"command": "make"}, ToolCallID: "call-1", Raw: struct{ Native bool }{true}}
	got := roundTrip(t, EventMessage(2, event))
	require.NotNil(t, got.Event)
	event.Raw = nil
	assert.Equal(t, event, got.DecodedEvent())
	assert.Equal(t, 2, got.Iteration)

	report := api.NewNodeReport(api.VerifyKindFixture, "fixture", api.VerifyNode{Name: "acceptance", Running: true})
	report.Iteration = 1
	assert.Equal(t, report, *roundTrip(t, ProgressMessage(report)).Progress)

	result := promptrun.Result{
		Response:        &api.Response{Text: "done", StructuredData: map[string]any{"summary": "Built it."}, Raw: "native", Workspace: &api.Workspace{Cwd: "/workspace/src"}},
		Verdicts:        []agent.VerifyResult{{Valid: true, Report: &report, Iteration: 1}},
		Report:          &report,
		Passed:          true,
		SessionID:       "provider-session",
		Model:           "claude-sonnet-5",
		Usage:           api.Usage{InputTokens: 7, OutputTokens: 3},
		CostUSD:         0.25,
		Duration:        90 * time.Second,
		StructuredData:  map[string]any{"summary": "Built it."},
		TerminalOutcome: &api.TerminalOutcome{Kind: api.TerminalOutcomeQuestions},
		Loop: &ai.LoopResult{StopReason: "condition-met", TotalCost: 0.25, Iterations: []*ai.LoopIteration{
			{Iteration: 1, SessionID: "provider-session", CostUSD: 0.25, Success: true, Events: []api.Event{{Kind: api.EventText, Text: "done", Raw: "native"}}},
			{Iteration: 2, Err: errBoom},
		}},
	}
	got = roundTrip(t, ResultMessage(result, nil))
	decoded := got.Result.Unwrap()
	require.NotNil(t, decoded.Loop)
	require.Len(t, decoded.Loop.Iterations, 2)
	assert.EqualError(t, decoded.Loop.Iterations[1].Err, "boom", "an iteration's error crosses as its message")
	result.Response.Raw = nil
	result.Loop.Iterations[0].Events[0].Raw = nil
	result.Loop.Iterations[1].Err = decoded.Loop.Iterations[1].Err
	assert.Equal(t, result, decoded)
	assert.Empty(t, got.Error)

	failed := roundTrip(t, ResultMessage(promptrun.Result{}, errBoom))
	assert.Equal(t, "boom", failed.Error)
}
