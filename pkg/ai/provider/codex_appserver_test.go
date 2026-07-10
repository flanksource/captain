package provider

import (
	"encoding/json"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/commons-db/shell"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCodexAppServer_Defaults(t *testing.T) {
	c, err := NewCodexAppServer("")
	require.NoError(t, err)
	assert.Equal(t, CodexCLIDefaultModel, c.GetModel())
	assert.Equal(t, ai.BackendCodexAgent, c.GetBackend())

	c2, err := NewCodexAppServer("gpt-5.4")
	require.NoError(t, err)
	assert.Equal(t, "gpt-5.4", c2.GetModel())
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
			name:   "command output delta becomes tool use",
			method: "item/commandExecution/outputDelta",
			params: `{"delta":"line1\n","itemId":"cmd-1"}`,
			want:   ai.EventToolUse,
			check: func(t *testing.T, ev ai.Event) {
				assert.Equal(t, "line1\n", ev.Input["delta"])
				tu, ok := ev.Raw.(claude.ToolUse)
				require.True(t, ok)
				assert.Equal(t, "cmd-1", tu.SessionID)
			},
		},
		{
			name:   "agent message item completed becomes text",
			method: "item/completed",
			params: `{"item":{"id":"i1","type":"agentMessage","text":"final answer"}}`,
			want:   ai.EventText,
			check:  func(t *testing.T, ev ai.Event) { assert.Equal(t, "final answer", ev.Text) },
		},
		{
			name:   "command execution item completed becomes tool use",
			method: "item/completed",
			params: `{"item":{"id":"c1","type":"commandExecution","command":"ls -la"}}`,
			want:   ai.EventToolUse,
			check: func(t *testing.T, ev ai.Event) {
				assert.Equal(t, "ls -la", ev.Tool)
				assert.Equal(t, "ls -la", ev.Input["command"])
				tu, ok := ev.Raw.(claude.ToolUse)
				require.True(t, ok)
				assert.Equal(t, "codex", tu.Source)
			},
		},
		{
			name:   "command execution item started becomes tool use",
			method: "item/started",
			params: `{"item":{"id":"c1","type":"commandExecution","command":"pwd"}}`,
			want:   ai.EventToolUse,
			check:  func(t *testing.T, ev ai.Event) { assert.Equal(t, "pwd", ev.Tool) },
		},
		{
			name:   "mcp tool call item uses tool name",
			method: "item/completed",
			params: `{"item":{"id":"m1","type":"mcpToolCall","tool":"search"}}`,
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
			ev, ok := mapAppServerNotification(tc.method, json.RawMessage(tc.params), "gpt-5", &ai.Usage{})
			if tc.drop {
				assert.False(t, ok, "expected notification to be dropped, got %+v", ev)
				return
			}
			require.True(t, ok, "expected an event for %s", tc.method)
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
	c, err := NewCodexAppServer("gpt-5")
	require.NoError(t, err)
	ts := &turnState{
		ch:           make(chan ai.Event, 16),
		usage:        &ai.Usage{},
		model:        "gpt-5",
		streamed:     map[string]bool{},
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

// A structured turn attaches the final agentMessage JSON to the terminal result.
func TestHandleNotification_StructuredOutput(t *testing.T) {
	schema, err := ai.SchemaJSONFor(api.Prompt{Schema: &struct {
		Answer string `json:"answer"`
	}{}})
	require.NoError(t, err)

	c, ts := activeTurn(t, schema)
	// The final agent message's text IS the validated JSON (codex has no separate
	// structured field).
	c.handleNotification("item/completed",
		json.RawMessage(`{"item":{"id":"a1","type":"agentMessage","text":"{\"answer\":\"42\"}"}}`))
	c.handleNotification("turn/completed",
		json.RawMessage(`{"threadId":"t","turn":{"id":"u","status":"completed"}}`))

	result := resultEvent(t, drainEvents(ts))
	require.NotEmpty(t, result.StructuredData, "structured turn should carry the final JSON on the result")
	assert.JSONEq(t, `{"answer":"42"}`, string(result.StructuredData))
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

	ev, ok := mapAppServerNotification("error", json.RawMessage(params), "gpt-5", &ai.Usage{})
	require.True(t, ok)
	assert.Equal(t, ai.EventError, ev.Kind)
	assert.Equal(t, nested, ev.Error, "stringified upstream error payload should be unwrapped one level")
}

func TestMapAppServerNotification_TurnFailed(t *testing.T) {
	ev, ok := mapAppServerNotification("turn/failed", json.RawMessage(`{"error":{"message":"boom"}}`), "m", &ai.Usage{})
	require.True(t, ok)
	assert.Equal(t, ai.EventError, ev.Kind)
	assert.Equal(t, "boom", ev.Error)
}

// Token usage folded from thread/tokenUsage/updated must surface on the
// subsequent turn/completed result event.
func TestMapAppServerNotification_UsageFolding(t *testing.T) {
	usage := &ai.Usage{}

	_, ok := mapAppServerNotification(
		"thread/tokenUsage/updated",
		json.RawMessage(`{"tokenUsage":{"total":{"inputTokens":120,"outputTokens":40,"cachedInputTokens":12,"reasoningOutputTokens":7}}}`),
		"m", usage)
	assert.False(t, ok, "token usage update emits no event")
	assert.Equal(t, 120, usage.InputTokens)
	assert.Equal(t, 40, usage.OutputTokens)
	assert.Equal(t, 12, usage.CacheReadTokens)
	assert.Equal(t, 7, usage.ReasoningTokens)

	ev, ok := mapAppServerNotification("turn/completed", json.RawMessage(`{"threadId":"t","turn":{"id":"u"}}`), "m", usage)
	require.True(t, ok)
	require.NotNil(t, ev.Usage, "turn/completed should carry the folded usage")
	assert.Equal(t, 120, ev.Usage.InputTokens)
	assert.Equal(t, 40, ev.Usage.OutputTokens)
	assert.Equal(t, 12, ev.Usage.CacheReadTokens)
	assert.Equal(t, 7, ev.Usage.ReasoningTokens)
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

func TestAppServerStreamedAgentMessage(t *testing.T) {
	streamed := map[string]bool{"i1": true}

	assert.True(t, appServerStreamedAgentMessage(
		json.RawMessage(`{"item":{"id":"i1","type":"agentMessage","text":"x"}}`), streamed),
		"completed agent message whose deltas streamed should be deduped")

	assert.False(t, appServerStreamedAgentMessage(
		json.RawMessage(`{"item":{"id":"i2","type":"agentMessage","text":"x"}}`), streamed),
		"a different item id was not streamed")

	assert.False(t, appServerStreamedAgentMessage(
		json.RawMessage(`{"item":{"id":"i1","type":"commandExecution"}}`), streamed),
		"non agent-message items are never deduped")
}

func TestThreadID(t *testing.T) {
	tid := func(s string) string { return parseAppServerNotif(json.RawMessage(s)).threadID() }
	assert.Equal(t, "nested", tid(`{"thread":{"id":"nested"}}`))
	assert.Equal(t, "sess", tid(`{"thread":{"sessionId":"sess"}}`))
	assert.Equal(t, "flat", tid(`{"threadId":"flat"}`))
	assert.Equal(t, "snake", tid(`{"thread_id":"snake"}`))
	assert.Equal(t, "", tid(`{}`))
}

func TestComposePrompt(t *testing.T) {
	assert.Equal(t, "task", composePrompt(req(api.Prompt{User: "task"})))
	assert.Equal(t, "be brief\n\ntask", composePrompt(req(api.Prompt{User: "task", System: "be brief"})))
	assert.Equal(t, "task\n\ntail", composePrompt(req(api.Prompt{User: "task", AppendSystem: "tail"})))
	assert.Equal(t, "sys\n\ntask\n\ntail",
		composePrompt(req(api.Prompt{User: "task", System: "sys", AppendSystem: "tail"})))
}

// req builds an ai.Request carrying only the given prompt, keeping the nested
// api.Spec literal out of the table tests above.
func req(p api.Prompt) ai.Request {
	return ai.Request{Prompt: p}
}

func TestBuildTurnStartParams(t *testing.T) {
	p := buildTurnStartParams("gpt-5", ai.Request{
		Prompt: api.Prompt{User: "hi"},
		Model:  api.Model{Effort: api.EffortUltra},
	}, "thread-1", nil)
	assert.Equal(t, "thread-1", p["threadId"])
	assert.Equal(t, "gpt-5", p["model"])
	assert.Equal(t, "ultra", p["effort"])

	input, ok := p["input"].([]map[string]any)
	require.True(t, ok, "input must be a slice of text elements")
	require.Len(t, input, 1)
	assert.Equal(t, "text", input[0]["type"])
	assert.Equal(t, "hi", input[0]["text"])

	// No reasoning effort / empty model / nil schema should be omitted entirely.
	bare := buildTurnStartParams("", req(api.Prompt{User: "hi"}), "t", nil)
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

	p := buildTurnStartParams("gpt-5", request, "t", schema)
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

func TestBuildThreadStartParams_Safety(t *testing.T) {
	tests := []struct {
		name         string
		req          ai.Request
		wantSandbox  string
		wantApproval string
		wantEphem    bool
		wantNoMCP    bool
	}{
		{
			name:         "default is read-only on-request",
			req:          req(api.Prompt{User: "p"}),
			wantSandbox:  "read-only",
			wantApproval: "on-request",
		},
		{
			name: "edit maps to workspace-write",
			req: ai.Request{
				Prompt:      api.Prompt{User: "p"},
				Permissions: api.Permissions{Presets: []api.Preset{api.PresetEdit}},
			},
			wantSandbox:  "workspace-write",
			wantApproval: "on-request",
		},
		{
			name: "explicit permission mode skips workspace-write default",
			req: ai.Request{
				Prompt:      api.Prompt{User: "p"},
				Permissions: api.Permissions{Presets: []api.Preset{api.PresetEdit}, Mode: api.PermissionDefault},
			},
			wantSandbox:  "read-only",
			wantApproval: "on-request",
		},
		{
			name: "bypass permissions maps to danger-full-access never",
			req: ai.Request{
				Prompt:      api.Prompt{User: "p"},
				Permissions: api.Permissions{Mode: api.PermissionBypass},
			},
			wantSandbox:  "danger-full-access",
			wantApproval: "never",
		},
		{
			name: "no-memory sets ephemeral",
			req: ai.Request{
				Prompt: api.Prompt{User: "p"},
				Memory: api.Memory{SkipMemory: true},
			},
			wantSandbox:  "read-only",
			wantApproval: "on-request",
			wantEphem:    true,
		},
		{
			name: "bare sets ephemeral",
			req: ai.Request{
				Prompt: api.Prompt{User: "p"},
				Memory: api.Memory{Bare: true},
			},
			wantSandbox:  "read-only",
			wantApproval: "on-request",
			wantEphem:    true,
		},
		{
			name: "no-mcp sets empty mcp_servers override",
			req: ai.Request{
				Prompt:      api.Prompt{User: "p"},
				Permissions: api.Permissions{MCP: api.MCP{Disabled: true}},
			},
			wantSandbox:  "read-only",
			wantApproval: "on-request",
			wantNoMCP:    true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := buildThreadStartParams("gpt-5", tc.req)
			assert.Equal(t, tc.wantSandbox, p["sandbox"])
			assert.Equal(t, tc.wantApproval, p["approvalPolicy"])

			_, hasEphem := p["ephemeral"]
			assert.Equal(t, tc.wantEphem, hasEphem)

			cfg, hasCfg := p["config"].(map[string]any)
			assert.Equal(t, tc.wantNoMCP, hasCfg)
			if tc.wantNoMCP {
				assert.Equal(t, map[string]any{}, cfg["mcp_servers"])
			}
		})
	}
}

func TestBuildThreadStartParams_CwdAndModel(t *testing.T) {
	p := buildThreadStartParams("gpt-5", ai.Request{
		Prompt: api.Prompt{User: "p"},
		Setup:  &shell.Setup{Cwd: "/repo"},
	})
	assert.Equal(t, "/repo", p["cwd"])
	assert.Equal(t, "gpt-5", p["model"])

	noModel := buildThreadStartParams("", req(api.Prompt{User: "p"}))
	_, hasModel := noModel["model"]
	assert.False(t, hasModel, "empty model must be omitted")
}

func TestBuildResumeParams(t *testing.T) {
	p := buildResumeParams(ai.Request{
		SessionID: "thread-9",
		Setup:     &shell.Setup{Cwd: "/repo"},
	})
	assert.Equal(t, "thread-9", p["threadId"])
	assert.Equal(t, "/repo", p["cwd"])
}

func TestHandleApproval_AutoApproves(t *testing.T) {
	c, err := NewCodexAppServer("m")
	require.NoError(t, err)

	tests := []struct {
		method string
		key    string
		want   any
	}{
		{"execCommandApproval", "decision", "approved"},
		{"applyPatchApproval", "decision", "approved"},
		{"item/commandExecution/requestApproval", "decision", "accept"},
		{"item/fileChange/requestApproval", "decision", "accept"},
		{"some/unknown/approval", "decision", "approved"},
	}
	for _, tc := range tests {
		t.Run(tc.method, func(t *testing.T) {
			res, rpcErr := c.handleApproval(tc.method, nil)
			assert.Nil(t, rpcErr)
			m, ok := res.(map[string]string)
			require.True(t, ok, "decision approvals return a string map")
			assert.Equal(t, tc.want, m[tc.key])
		})
	}

	res, rpcErr := c.handleApproval("item/permissions/requestApproval", nil)
	assert.Nil(t, rpcErr)
	perm, ok := res.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "turn", perm["scope"])
	assert.NotNil(t, perm["permissions"])
}
