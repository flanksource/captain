package claudeagent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProvider_StreamLifecycle(t *testing.T) {
	withFakeAgentProcess(t)

	p, err := New(ai.Config{Model: api.Model{Name: "claude-sonnet-5"}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })

	assert.Equal(t, ai.BackendClaudeAgent, p.GetBackend())
	assert.Equal(t, "claude-sonnet-5", p.GetModel())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	events, err := p.ExecuteStream(ctx, ai.Request{Prompt: api.Prompt{User: "hello"}})
	require.NoError(t, err)

	var kinds []ai.EventKind
	var result *ai.Event
	for ev := range events {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == ai.EventResult {
			cp := ev
			result = &cp
		}
	}

	assert.Contains(t, kinds, ai.EventText)
	assert.Contains(t, kinds, ai.EventToolUse)
	require.NotNil(t, result, "expected a terminal result event")
	assert.True(t, result.Success)
	assert.Equal(t, "fake-sess", result.SessionID)
	require.NotNil(t, result.Usage)
	assert.Equal(t, 10, result.Usage.InputTokens)
	assert.Equal(t, 5, result.Usage.OutputTokens)
}

func TestProvider_ExecuteCoalesce(t *testing.T) {
	withFakeAgentProcess(t)

	p, err := New(ai.Config{Model: api.Model{Name: "claude-sonnet-5"}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := p.Execute(ctx, ai.Request{Prompt: api.Prompt{User: "hello"}})
	require.NoError(t, err)
	assert.Equal(t, "hi from fake", resp.Text)
	assert.Equal(t, ai.BackendClaudeAgent, resp.Backend)
	assert.Equal(t, 10, resp.Usage.InputTokens)
}

func TestProvider_ErrorIncludesCapturedProcessOutput(t *testing.T) {
	withFakeAgentProcessEnv(t, map[string]string{
		fakeServerEnv: "1",
		fakeModeEnv:   "error-output",
	})

	p, err := New(ai.Config{Model: api.Model{Name: "claude-sonnet-5"}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })

	events, err := p.ExecuteStream(t.Context(), ai.Request{Prompt: api.Prompt{User: "fail"}})
	require.NoError(t, err)

	var eventError string
	for event := range events {
		if event.Kind == ai.EventError {
			eventError = event.Error
		}
	}
	assert.Contains(t, eventError, "Claude Code process exited with code 1")
	assert.Contains(t, eventError, "stderr:\nclaude subprocess authentication detail")
	assert.Contains(t, eventError, "stdout (JSON-RPC):")
}

// companyInfo is the structured-output target for the round-trip test.
type companyInfo struct {
	CompanyName    string `json:"company_name"`
	FoundedYear    int    `json:"founded_year"`
	ReceivedSchema bool   `json:"received_schema"`
}

// TestProvider_StructuredOutput drives the structured-output path end to end: the
// provider derives a JSON schema from the target, transmits it on initialize
// (the fake echoes that it arrived), and unmarshals the SDK's structured_output
// back into the caller's Go struct with Text cleared.
func TestProvider_StructuredOutput(t *testing.T) {
	withFakeAgentProcessEnv(t, map[string]string{fakeServerEnv: "1", fakeModeEnv: "structured"})

	p, err := New(ai.Config{Model: api.Model{Name: "claude-sonnet-5"}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var out companyInfo
	resp, err := p.Execute(ctx, ai.Request{Prompt: api.Prompt{User: "research Anthropic", Schema: &out}})
	require.NoError(t, err)

	assert.True(t, out.ReceivedSchema, "provider should transmit the derived schema on initialize")
	assert.Equal(t, "Anthropic", out.CompanyName)
	assert.Equal(t, 2021, out.FoundedYear)

	assert.Same(t, &out, resp.StructuredData, "StructuredData points at the populated target")
	assert.Empty(t, resp.Text, "structured runs clear the text answer")
}

// TestRequestSchemaJSON checks the schema captain derives from a structured
// target and that initializeParams carries it as outputSchema.
func TestRequestSchemaJSON(t *testing.T) {
	type plan struct {
		Summary string   `json:"summary"`
		Steps   []string `json:"steps"`
	}

	t.Run("nil target is text mode", func(t *testing.T) {
		raw, err := requestSchemaJSON(ai.Request{Prompt: api.Prompt{User: "x"}})
		require.NoError(t, err)
		assert.Nil(t, raw)
	})

	t.Run("struct target derives an object schema wired into initializeParams", func(t *testing.T) {
		raw, err := requestSchemaJSON(ai.Request{Prompt: api.Prompt{Schema: &plan{}}})
		require.NoError(t, err)
		require.NotEmpty(t, raw)

		p := &Provider{model: "claude-sonnet-5", sessionSchema: raw}
		ip := p.initializeParams(ai.Request{Prompt: api.Prompt{User: "x", Schema: &plan{}}})
		require.NotEmpty(t, ip.OutputSchema, "outputSchema should be set from the session schema")

		var decoded map[string]any
		require.NoError(t, json.Unmarshal(ip.OutputSchema, &decoded))
		assert.Equal(t, "object", decoded["type"])
		props, ok := decoded["properties"].(map[string]any)
		require.True(t, ok)
		assert.Contains(t, props, "summary")
		assert.Contains(t, props, "steps")
	})

	t.Run("raw schema is sanitized for anthropic structured output", func(t *testing.T) {
		raw, err := requestSchemaJSON(ai.Request{Prompt: api.Prompt{
			SchemaJSON: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string","maxLength":12}}}`),
		}})
		require.NoError(t, err)
		var decoded map[string]any
		require.NoError(t, json.Unmarshal(raw, &decoded))
		answer := decoded["properties"].(map[string]any)["answer"].(map[string]any)
		if _, ok := answer["maxLength"]; ok {
			t.Fatalf("request schema should strip maxLength key, got %s", raw)
		}
		assert.NotEmpty(t, answer["description"])
	})

	t.Run("non-struct target fails loudly", func(t *testing.T) {
		_, err := requestSchemaJSON(ai.Request{Prompt: api.Prompt{Schema: "not a struct"}})
		require.Error(t, err)
	})
}

func TestProvider_MultiTurnSerialized(t *testing.T) {
	withFakeAgentProcess(t)

	p, err := New(ai.Config{Model: api.Model{Name: "claude-sonnet-5"}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Two sequential turns over the same supervised session must both complete.
	for turn := 0; turn < 2; turn++ {
		events, err := p.ExecuteStream(ctx, ai.Request{Prompt: api.Prompt{User: "go"}})
		require.NoError(t, err)
		var sawResult bool
		for ev := range events {
			if ev.Kind == ai.EventResult {
				sawResult = true
				assert.True(t, ev.Success)
			}
		}
		assert.Truef(t, sawResult, "turn %d should produce a result", turn)
	}
}

// TestProvider_CanUseTool drives the can_use_tool control round-trip end to end:
// the fake agent emits a can_use_tool request, the provider routes it to the
// request's CanUseTool callback, surfaces an EventPermission, and the decision
// round-trips back to the agent (which echoes it into the result).
func TestProvider_CanUseTool(t *testing.T) {
	withFakeAgentProcessEnv(t, map[string]string{fakeServerEnv: "1", fakeModeEnv: "approval"})

	var gotReq ai.PermissionRequest
	called := make(chan struct{}, 1)
	canUseTool := func(_ context.Context, r ai.PermissionRequest) (ai.PermissionDecision, error) {
		gotReq = r
		called <- struct{}{}
		return ai.PermissionDecision{Allow: true, UpdatedInput: map[string]any{"command": "ls -la"}}, nil
	}

	// CanUseTool is a runtime concern, set on the provider's Config (not the request).
	p, err := New(ai.Config{Model: api.Model{Name: "claude-sonnet-5"}, CanUseTool: canUseTool})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	events, err := p.ExecuteStream(ctx, ai.Request{Prompt: api.Prompt{User: "use a tool"}})
	require.NoError(t, err)

	var sawPermission bool
	var permEvent ai.Event
	var result *ai.Event
	for ev := range events {
		switch ev.Kind {
		case ai.EventPermission:
			sawPermission = true
			permEvent = ev
		case ai.EventResult:
			cp := ev
			result = &cp
		}
	}

	// The callback fired with the requested tool and the live session id, and the
	// fake's input was forwarded verbatim.
	select {
	case <-called:
	default:
		t.Fatal("CanUseTool callback was not invoked")
	}
	assert.Equal(t, "Bash", gotReq.Tool)
	assert.Equal(t, "tu1", gotReq.ToolUseID)
	assert.Equal(t, "fake-sess", gotReq.SessionID)
	assert.Equal(t, "ls", gotReq.Input["command"])

	// The same request surfaced as an observable EventPermission.
	assert.True(t, sawPermission, "expected an EventPermission")
	assert.Equal(t, "Bash", permEvent.Tool)

	// The allow decision (with the updated input) round-tripped back to the agent.
	require.NotNil(t, result, "expected a terminal result event")
	assert.True(t, result.Success)
	resultText, _ := result.Input["result"].(string)
	assert.Contains(t, resultText, `"allow":true`)
	assert.Contains(t, resultText, "ls -la")
}

// TestProvider_PlanModeExitPlanModeAutoDenied verifies the shared plan-mode
// terminal policy: in a plan-only run the ExitPlanMode can_use_tool is answered
// by the provider itself (deny + guidance) — the host broker is never invoked,
// no EventPermission is surfaced, and the already-streamed tool_use still
// yields the plan terminal outcome.
func TestProvider_PlanModeExitPlanModeAutoDenied(t *testing.T) {
	withFakeAgentProcessEnv(t, map[string]string{fakeServerEnv: "1", fakeModeEnv: "plan-approval"})

	canUseTool := func(_ context.Context, r ai.PermissionRequest) (ai.PermissionDecision, error) {
		t.Errorf("CanUseTool must not be invoked for ExitPlanMode in plan mode, got %s", r.Tool)
		return ai.PermissionDecision{Allow: true}, nil
	}

	p, err := New(ai.Config{Model: api.Model{Name: "claude-sonnet-5"}, CanUseTool: canUseTool})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	events, err := p.ExecuteStream(ctx, ai.Request{
		Prompt:      api.Prompt{User: "plan it"},
		Permissions: api.Permissions{Mode: api.PermissionPlan},
	})
	require.NoError(t, err)

	var outcome *ai.TerminalOutcome
	var result *ai.Event
	for ev := range events {
		switch ev.Kind {
		case ai.EventPermission:
			t.Errorf("plan-terminal ExitPlanMode must not surface an EventPermission, got %s", ev.Tool)
		case ai.EventToolUse:
			parsed, perr := ai.TerminalOutcomeFromEvent(ev)
			require.NoError(t, perr)
			if parsed != nil {
				outcome = parsed
			}
		case ai.EventResult:
			cp := ev
			result = &cp
		}
	}

	// The streamed tool_use still derives the plan terminal outcome.
	require.NotNil(t, outcome, "expected the ExitPlanMode tool_use to stream")
	assert.Equal(t, ai.TerminalOutcomePlan, outcome.Kind)
	assert.Equal(t, "/repo/.claude/plans/x.md", outcome.Plan.Path)

	// The provider answered the can_use_tool itself with the deny + guidance.
	require.NotNil(t, result, "expected a terminal result event")
	resultText, _ := result.Input["result"].(string)
	assert.Contains(t, resultText, `"allow":false`)
	assert.Contains(t, resultText, "Plan captured for human review")
}

// TestProvider_InterruptNoKill verifies a cancelled turn is ended by the
// interrupt control request — the fake records receiving it — rather than by
// killing the process.
func TestProvider_InterruptNoKill(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "interrupted")
	withFakeAgentProcessEnv(t, map[string]string{
		fakeServerEnv: "1",
		fakeModeEnv:   "hang",
		fakeMarkerEnv: marker,
	})

	p, err := New(ai.Config{Model: api.Model{Name: "claude-sonnet-5"}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, err := p.ExecuteStream(ctx, ai.Request{Prompt: api.Prompt{User: "go"}})
	require.NoError(t, err)

	var sawError bool
	for ev := range events {
		if ev.Kind == ai.EventText {
			// The turn is now hanging; cancelling triggers the interrupt path.
			cancel()
		}
		if ev.Kind == ai.EventError {
			sawError = true
		}
	}

	assert.True(t, sawError, "cancelled turn should surface an error event")
	// interrupt() completes before the error is emitted, so by now the fake has
	// written the marker — proof the graceful interrupt reached it.
	require.FileExists(t, marker)
}

func TestProvider_SteerKeepsTurnOpenForEveryAcceptedPrompt(t *testing.T) {
	withFakeAgentProcessEnv(t, map[string]string{
		fakeServerEnv: "1",
		fakeModeEnv:   "steer",
	})

	p, err := New(ai.Config{Model: api.Model{Name: "claude-sonnet-5"}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })

	events, err := p.ExecuteStream(context.Background(), ai.Request{Prompt: api.Prompt{User: "first"}})
	require.NoError(t, err)
	for event := range events {
		if event.Kind == ai.EventText {
			break
		}
	}
	require.NoError(t, p.Steer(context.Background(), ai.Request{Prompt: api.Prompt{User: "btw"}}))

	results := 0
	for event := range events {
		if event.Kind == ai.EventResult {
			results++
		}
	}
	assert.Equal(t, 2, results)
}
