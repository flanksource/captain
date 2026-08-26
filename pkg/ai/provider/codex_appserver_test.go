package provider

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCodexAppServer_Defaults(t *testing.T) {
	c, err := NewCodexAppServer(ai.Config{})
	require.NoError(t, err)
	assert.Equal(t, CodexCLIDefaultModel, c.GetModel())
	assert.Equal(t, ai.BackendCodexAgent, c.GetBackend())

	c2, err := NewCodexAppServer(ai.Config{Model: api.Model{Name: "gpt-5.4"}})
	require.NoError(t, err)
	assert.Equal(t, "gpt-5.4", c2.GetModel())
}

func TestCodexAppServerProcessHonoursAPIURL(t *testing.T) {
	process := newCodexAppServerProcess(ai.Config{
		APIURL: "http://127.0.0.1:4020/v1", APIKey: "captain-mock",
	})
	assert.Equal(t, "codex", process.Cmd)
	assert.Equal(t, []string{
		"app-server",
		"-c", "model_provider=captain",
		"-c", "model_providers.captain.name=captain",
		"-c", "model_providers.captain.base_url=http://127.0.0.1:4020/v1",
		"-c", "model_providers.captain.env_key=OPENAI_API_KEY",
		"-c", "model_providers.captain.wire_api=responses",
	}, process.Args)
	assert.Equal(t, "captain-mock", process.Env["OPENAI_API_KEY"])
}

func TestAppServerProcessErrorIncludesStderr(t *testing.T) {
	err := appServerProcessError(errors.New("jsonrpc: client closed"), " state runtime unavailable \n")
	assert.EqualError(t, err, "jsonrpc: client closed: state runtime unavailable")
}

func TestMapAppServerNotification_Kinds(t *testing.T) {
	tests := []struct {
		name   string
		method string
		params string
		want   ai.EventKind
		drop   bool
		check  func(t *testing.T, ev ai.Event)
	}{
		{
			name:   "thread/started carries thread.id as session id",
			method: "thread/started",
			params: `{"thread":{"id":"019e-thread","sessionId":"sess-1"}}`,
			want:   ai.EventSystem,
			check: func(t *testing.T, ev ai.Event) {
				assert.Equal(t, "019e-thread", ev.SessionID)
				assert.Equal(t, "SessionInit", ev.Tool)
				tu, ok := ev.Raw.(claude.ToolUse)
				require.True(t, ok, "Raw should be a claude.ToolUse")
				assert.Equal(t, "codex", tu.Source)
				assert.Equal(t, "019e-thread", tu.SessionID)
			},
		},
		{
			name:   "thread/started falls back to flat threadId",
			method: "thread/started",
			params: `{"threadId":"flat-thread"}`,
			want:   ai.EventSystem,
			check: func(t *testing.T, ev ai.Event) {
				assert.Equal(t, "flat-thread", ev.SessionID)
			},
		},
		{
			name:   "agent message delta becomes text",
			method: "item/agentMessage/delta",
			params: `{"delta":"hel","itemId":"i1","threadId":"t","turnId":"u"}`,
			want:   ai.EventText,
			check:  func(t *testing.T, ev ai.Event) { assert.Equal(t, "hel", ev.Text) },
		},
		{
			name:   "empty agent message delta is dropped",
			method: "item/agentMessage/delta",
			params: `{"delta":"","itemId":"i1"}`,
			drop:   true,
		},
		{
			name:   "reasoning text delta becomes thinking",
			method: "item/reasoning/textDelta",
			params: `{"delta":"because","itemId":"i1","contentIndex":0}`,
			want:   ai.EventThinking,
			check:  func(t *testing.T, ev ai.Event) { assert.Equal(t, "because", ev.Text) },
		},
		{
			name:   "command output delta is buffered by the turn",
			method: "item/commandExecution/outputDelta",
			params: `{"delta":"line1\n","itemId":"cmd-1"}`,
			drop:   true,
		},
		{
			name:   "agent message item completed becomes text",
			method: "item/completed",
			params: `{"item":{"id":"i1","type":"agentMessage","text":"final answer"}}`,
			want:   ai.EventText,
			check:  func(t *testing.T, ev ai.Event) { assert.Equal(t, "final answer", ev.Text) },
		},
		{
			name:   "command execution item completed becomes tool result",
			method: "item/completed",
			params: `{"threadId":"thread-1","item":{"id":"c1","type":"commandExecution","command":"ls -la","status":"completed","exitCode":0}}`,
			want:   ai.EventToolResult,
			check: func(t *testing.T, ev ai.Event) {
				assert.Equal(t, "c1", ev.ToolCallID)
				assert.True(t, ev.Success)
				tu, ok := ev.Raw.(claude.ToolUse)
				require.True(t, ok)
				assert.Equal(t, "c1", tu.ToolUseID)
			},
		},
		{
			name:   "command execution item started becomes tool use",
			method: "item/started",
			params: `{"threadId":"thread-1","item":{"id":"c1","type":"commandExecution","command":"pwd","status":"inProgress"}}`,
			want:   ai.EventToolUse,
			check: func(t *testing.T, ev ai.Event) {
				assert.Equal(t, "Bash", ev.Tool)
				assert.Equal(t, "pwd", ev.Input["command"])
			},
		},
		{
			name:   "mcp tool call item uses tool name",
			method: "item/started",
			params: `{"threadId":"thread-1","item":{"id":"m1","type":"mcpToolCall","tool":"search","arguments":{"query":"captain"},"status":"inProgress"}}`,
			want:   ai.EventToolUse,
			check:  func(t *testing.T, ev ai.Event) { assert.Equal(t, "search", ev.Tool) },
		},
		{
			name:   "completed reasoning item is dropped (streamed via deltas)",
			method: "item/completed",
			params: `{"item":{"id":"r1","type":"reasoning","summary":"thought"}}`,
			drop:   true,
		},
		{
			name:   "user message item is dropped",
			method: "item/completed",
			params: `{"item":{"id":"u1","type":"userMessage"}}`,
			drop:   true,
		},
		{
			name:   "turn completed is a successful result",
			method: "turn/completed",
			params: `{"threadId":"t1","turn":{"id":"u1","status":"completed"}}`,
			want:   ai.EventResult,
			check: func(t *testing.T, ev ai.Event) {
				assert.True(t, ev.Success)
				tu, ok := ev.Raw.(claude.ToolUse)
				require.True(t, ok)
				assert.Equal(t, "t1", tu.SessionID)
			},
		},
		{
			name:   "token usage update folds without emitting",
			method: "thread/tokenUsage/updated",
			params: `{"threadId":"t","turnId":"u","tokenUsage":{"total":{"inputTokens":10,"outputTokens":5}}}`,
			drop:   true,
		},
		{
			name:   "unknown notification is dropped",
			method: "thread/status/changed",
			params: `{"status":"idle"}`,
			drop:   true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			events := mapAppServerNotification(tc.method, json.RawMessage(tc.params), appServerEventContext{
				Model: "gpt-5", Usage: &ai.Usage{},
			})
			if tc.drop {
				assert.Empty(t, events, "expected notification to be dropped, got %+v", events)
				return
			}
			require.Len(t, events, 1, "expected one event for %s", tc.method)
			ev := events[0]
			assert.Equal(t, tc.want, ev.Kind)
			assert.Equal(t, "gpt-5", ev.Model)
			if tc.check != nil {
				tc.check(t, ev)
			}
		})
	}
}

// drainEvents non-blockingly collects everything buffered on a turn's channel.
func drainEvents(ts *turnState) []ai.Event {
	var out []ai.Event
	for {
		select {
		case ev := <-ts.ch:
			out = append(out, ev)
		default:
			return out
		}
	}
}

// activeTurn wires a turnState onto a fresh provider so handleNotification can
// route to it, mirroring what ExecuteStream sets up.
func activeTurn(t *testing.T, schema json.RawMessage) (*CodexAppServer, *turnState) {
	t.Helper()
	c, err := NewCodexAppServer(ai.Config{Model: api.Model{Name: "gpt-5"}})
	require.NoError(t, err)
	ts := &turnState{
		ch:           make(chan ai.Event, 16),
		usage:        &ai.Usage{},
		model:        "gpt-5",
		streamed:     map[string]string{},
		toolOutput:   map[string]string{},
		terminal:     make(chan struct{}),
		outputSchema: schema,
	}
	c.setActive(ts)
	return c, ts
}

func resultEvent(t *testing.T, evs []ai.Event) ai.Event {
	t.Helper()
	for _, ev := range evs {
		if ev.Kind == ai.EventResult {
			return ev
		}
	}
	t.Fatalf("no EventResult in %+v", evs)
	return ai.Event{}
}

// A structured turn attaches the final agentMessage JSON only to the terminal result.
func TestHandleNotification_StructuredOutput(t *testing.T) {
	schema, err := ai.SchemaJSONFor(api.Prompt{Schema: &struct {
		Answer string `json:"answer"`
	}{}})
	require.NoError(t, err)

	c, ts := activeTurn(t, schema)
	c.handleNotification("item/agentMessage/delta",
		json.RawMessage(`{"itemId":"a1","delta":"{\"answer\":"}`))
	c.handleNotification("item/agentMessage/delta",
		json.RawMessage(`{"itemId":"a1","delta":"\"42\"}"}`))
	c.handleNotification("item/completed",
		json.RawMessage(`{"item":{"id":"a1","type":"agentMessage","text":"{\"answer\":\"42\"}"}}`))
	c.handleNotification("turn/completed",
		json.RawMessage(`{"threadId":"t","turn":{"id":"u","status":"completed"}}`))

	events := drainEvents(ts)
	var resultCount int
	for _, ev := range events {
		assert.NotEqual(t, ai.EventText, ev.Kind, "structured agent messages must not emit text progress")
		if ev.Kind == ai.EventResult {
			resultCount++
		}
	}
	assert.Equal(t, 1, resultCount, "structured turn should emit exactly one terminal result")
	result := resultEvent(t, events)
	assert.True(t, result.Success)
	require.NotEmpty(t, result.StructuredData, "structured turn should carry the final JSON on the result")
	assert.JSONEq(t, `{"answer":"42"}`, string(result.StructuredData))
}

func TestHandleNotification_StructuredOutputWithoutDeltas(t *testing.T) {
	schema := json.RawMessage(`{"type":"object"}`)
	c, ts := activeTurn(t, schema)
	c.handleNotification("item/completed",
		json.RawMessage(`{"item":{"id":"a1","type":"agentMessage","text":"{\"answer\":\"42\"}"}}`))
	c.handleNotification("turn/completed",
		json.RawMessage(`{"threadId":"t","turn":{"id":"u","status":"completed"}}`))

	events := drainEvents(ts)
	for _, ev := range events {
		assert.NotEqual(t, ai.EventText, ev.Kind, "completed structured message must not leak as text progress")
	}
	assert.JSONEq(t, `{"answer":"42"}`, string(resultEvent(t, events).StructuredData))
}

func TestHandleNotification_TextModeStreamsDeltasAndDeduplicatesCompletedMessage(t *testing.T) {
	c, ts := activeTurn(t, nil)
	c.handleNotification("item/agentMessage/delta",
		json.RawMessage(`{"itemId":"a1","delta":"plain "}`))
	c.handleNotification("item/agentMessage/delta",
		json.RawMessage(`{"itemId":"a1","delta":"answer"}`))
	c.handleNotification("item/completed",
		json.RawMessage(`{"item":{"id":"a1","type":"agentMessage","text":"plain answer"}}`))
	c.handleNotification("turn/completed",
		json.RawMessage(`{"threadId":"t","turn":{"id":"u","status":"completed"}}`))

	events := drainEvents(ts)
	var text []string
	for _, ev := range events {
		if ev.Kind == ai.EventText {
			text = append(text, ev.Text)
		}
	}
	assert.Equal(t, []string{"plain ", "answer"}, text)
	assert.Empty(t, resultEvent(t, events).StructuredData)
}

func TestHandleNotification_TextModeBackfillsUnstreamedCompletedSuffix(t *testing.T) {
	c, ts := activeTurn(t, nil)
	c.handleNotification("item/agentMessage/delta",
		json.RawMessage(`{"itemId":"a1","delta":"plain "}`))
	c.handleNotification("item/completed",
		json.RawMessage(`{"item":{"id":"a1","type":"agentMessage","text":"plain answer"}}`))

	events := drainEvents(ts)
	var text []string
	for _, event := range events {
		if event.Kind == ai.EventText {
			text = append(text, event.Text)
		}
	}
	assert.Equal(t, []string{"plain ", "answer"}, text)
}

// A text-mode turn (no schema) leaves the result's StructuredData empty.
func TestHandleNotification_NoStructuredWithoutSchema(t *testing.T) {
	c, ts := activeTurn(t, nil)
	c.handleNotification("item/completed",
		json.RawMessage(`{"item":{"id":"a1","type":"agentMessage","text":"plain answer"}}`))
	c.handleNotification("turn/completed",
		json.RawMessage(`{"threadId":"t","turn":{"id":"u","status":"completed"}}`))

	result := resultEvent(t, drainEvents(ts))
	assert.Empty(t, result.StructuredData, "text-mode turn must not attach structured output")
}

func TestMapAppServerNotification_ErrorUnwrapping(t *testing.T) {
	nested := `The 'gpt-5.5-codex' model is not supported when using Codex with a ChatGPT account.`
	params := `{"threadId":"t","turnId":"u","willRetry":false,"error":{"message":"{\"type\":\"error\",\"status\":400,\"error\":{\"type\":\"invalid_request_error\",\"message\":\"` + nested + `\"}}"}}`

	events := mapAppServerNotification("error", json.RawMessage(params), appServerEventContext{Model: "gpt-5", Usage: &ai.Usage{}})
	require.Len(t, events, 1)
	ev := events[0]
	assert.Equal(t, ai.EventError, ev.Kind)
	assert.Equal(t, nested, ev.Error, "stringified upstream error payload should be unwrapped one level")
}

func TestMapAppServerNotification_TurnFailed(t *testing.T) {
	events := mapAppServerNotification("turn/failed", json.RawMessage(`{"error":{"message":"boom"}}`), appServerEventContext{Model: "m", Usage: &ai.Usage{}})
	require.Len(t, events, 1)
	ev := events[0]
	assert.Equal(t, ai.EventError, ev.Kind)
	assert.Equal(t, "boom", ev.Error)
}

// Token usage folded from thread/tokenUsage/updated must surface on the
// subsequent turn/completed result event, normalized to captain's disjoint
// buckets: codex reports inputTokens inclusive of cachedInputTokens and
// outputTokens inclusive of reasoningOutputTokens, so foldUsage nets both out to
// avoid the cache/reasoning double-count (findings B1/B2).
func TestMapAppServerNotification_UsageFolding(t *testing.T) {
	usage := &ai.Usage{}

	ctx := appServerEventContext{Model: "m", Usage: usage}
	events := mapAppServerNotification(
		"thread/tokenUsage/updated",
		json.RawMessage(`{"tokenUsage":{"total":{"inputTokens":120,"outputTokens":40,"cachedInputTokens":12,"reasoningOutputTokens":7}}}`),
		ctx)
	assert.Empty(t, events, "token usage update emits no event")
	assert.Equal(t, 108, usage.InputTokens, "input net of cache (120-12)")
	assert.Equal(t, 33, usage.OutputTokens, "output net of reasoning (40-7)")
	assert.Equal(t, 12, usage.CacheReadTokens)
	assert.Equal(t, 7, usage.ReasoningTokens)

	events = mapAppServerNotification("turn/completed", json.RawMessage(`{"threadId":"t","turn":{"id":"u"}}`), ctx)
	require.Len(t, events, 1)
	ev := events[0]
	require.NotNil(t, ev.Usage, "turn/completed should carry the folded usage")
	assert.Equal(t, 108, ev.Usage.InputTokens)
	assert.Equal(t, 33, ev.Usage.OutputTokens)
	assert.Equal(t, 12, ev.Usage.CacheReadTokens)
	assert.Equal(t, 7, ev.Usage.ReasoningTokens)
	// Disjoint buckets: netted input+cache and output+reasoning recover the raw
	// codex totals, so pricing cannot bill the overlap twice.
	assert.Equal(t, 120, ev.Usage.InputTokens+ev.Usage.CacheReadTokens)
	assert.Equal(t, 40, ev.Usage.OutputTokens+ev.Usage.ReasoningTokens)
}

func TestAppServerErrorIsFatal(t *testing.T) {
	tests := []struct {
		name   string
		method string
		params string
		want   bool
	}{
		{"non-retryable error is fatal", "error", `{"willRetry":false}`, true},
		{"retryable error is not fatal", "error", `{"willRetry":true}`, false},
		{"turn failed is always fatal", "turn/failed", `{}`, true},
		{"other methods are never fatal", "turn/completed", `{}`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, appServerErrorIsFatal(tc.method, json.RawMessage(tc.params)))
		})
	}
}

func TestAppServerAgentMessageRemainder(t *testing.T) {
	streamed := map[string]string{"i1": "partial"}

	remainder, handled, err := appServerAgentMessageRemainder(
		json.RawMessage(`{"item":{"id":"i1","type":"agentMessage","text":"partial result"}}`), streamed)
	require.NoError(t, err)
	assert.True(t, handled)
	assert.Equal(t, " result", remainder)

	_, handled, err = appServerAgentMessageRemainder(
		json.RawMessage(`{"item":{"id":"i2","type":"agentMessage","text":"x"}}`), streamed)
	require.NoError(t, err)
	assert.False(t, handled)

	_, handled, err = appServerAgentMessageRemainder(
		json.RawMessage(`{"item":{"id":"i1","type":"commandExecution"}}`), streamed)
	require.NoError(t, err)
	assert.False(t, handled)
}

func TestThreadID(t *testing.T) {
	tid := func(s string) string { return parseAppServerNotif(json.RawMessage(s)).threadID() }
	assert.Equal(t, "nested", tid(`{"thread":{"id":"nested"}}`))
	assert.Equal(t, "sess", tid(`{"thread":{"sessionId":"sess"}}`))
	assert.Equal(t, "flat", tid(`{"threadId":"flat"}`))
	assert.Equal(t, "snake", tid(`{"thread_id":"snake"}`))
	assert.Equal(t, "", tid(`{}`))
}

// req builds an ai.Request carrying only the given prompt, keeping the nested
// api.Spec literal out of the table tests above.
func req(p api.Prompt) ai.Request {
	return ai.Request{Prompt: p}
}

func TestBuildTurnStartParams(t *testing.T) {
	p, err := buildTurnStartParams("gpt-5", ai.Request{
		Prompt: api.Prompt{User: "hi"},
		Model:  api.Model{Effort: api.EffortUltra},
	}, "thread-1", nil)
	require.NoError(t, err)
	assert.Equal(t, "thread-1", p["threadId"])
	assert.Equal(t, "gpt-5", p["model"])
	assert.Equal(t, "ultra", p["effort"])

	input, ok := p["input"].([]map[string]any)
	require.True(t, ok, "input must be a slice of text elements")
	require.Len(t, input, 1)
	assert.Equal(t, "text", input[0]["type"])
	assert.Equal(t, "hi", input[0]["text"])

	// No reasoning effort / empty model / nil schema should be omitted entirely.
	bare, err := buildTurnStartParams("", req(api.Prompt{User: "hi"}), "t", nil)
	require.NoError(t, err)
	_, hasModel := bare["model"]
	_, hasEffort := bare["effort"]
	_, hasSchema := bare["outputSchema"]
	assert.False(t, hasModel, "empty model must be omitted")
	assert.False(t, hasEffort, "absent reasoning effort must be omitted")
	assert.False(t, hasSchema, "nil schema must omit outputSchema")
}

func TestBuildTurnStartParams_OutputSchema(t *testing.T) {
	type answer struct {
		Answer string `json:"answer"`
		Detail string `json:"detail,omitempty"`
	}
	request := req(api.Prompt{User: "solve", Schema: &answer{}})
	schema, err := codexOutputSchema(request)
	require.NoError(t, err)

	p, err := buildTurnStartParams("gpt-5", request, "t", schema)
	require.NoError(t, err)
	require.Contains(t, p, "outputSchema", "a derived schema must be sent as outputSchema")

	// It must serialize to a JSON Schema object describing the target struct.
	raw, err := json.Marshal(p["outputSchema"])
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.Equal(t, "object", decoded["type"])
	props, ok := decoded["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, props, "answer")
	assert.Contains(t, props, "detail")
	assert.Equal(t, []any{"answer", "detail"}, decoded["required"])
	assert.Equal(t, false, decoded["additionalProperties"])
}
