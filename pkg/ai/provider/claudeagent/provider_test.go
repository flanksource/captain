package claudeagent

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/clicky/exec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeServerEnv switches the test binary into a fake JSON-RPC agent server when
// re-exec'd by the supervised process, so the provider lifecycle is exercised
// without npm, tsx or a real claude binary.
const fakeServerEnv = "CLAUDEAGENT_FAKE_SERVER"

// fakeModeEnv selects the fake server's turn behaviour: "" runs the default
// happy-path turn; "approval" emits a can_use_tool request and finishes only
// after the host replies; "hang" emits text then waits for an interrupt.
const fakeModeEnv = "CLAUDEAGENT_FAKE_MODE"

// fakeMarkerEnv, when set in "hang" mode, names a file the fake creates when it
// receives an interrupt — proof the graceful control request arrived (no kill).
const fakeMarkerEnv = "CLAUDEAGENT_FAKE_MARKER"

func TestMain(m *testing.M) {
	if os.Getenv(fakeServerEnv) == "1" {
		runFakeServer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runFakeServer speaks the agent.ts JSON-RPC protocol over stdio: it answers
// initialize/prompt/interrupt/shutdown and pushes a scripted set of turn
// notifications so the Go provider has a realistic stream to map. The turn shape
// is selected by fakeModeEnv so a single binary covers the happy path, the
// can_use_tool round-trip, and the interrupt-without-kill path.
func runFakeServer() {
	mode := os.Getenv(fakeModeEnv)
	marker := os.Getenv(fakeMarkerEnv)

	enc := func(obj map[string]any) {
		b, _ := json.Marshal(obj)
		_, _ = os.Stdout.Write(append(b, '\n'))
	}
	id := func(raw json.RawMessage) any {
		if len(raw) == 0 {
			return nil
		}
		return raw
	}
	completed := func(resultText string) {
		enc(map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{
			"success": true, "session_id": "fake-sess", "cost_usd": 0.01,
			"result_text": resultText,
			"usage":       map[string]any{"input_tokens": 10, "output_tokens": 5},
		}})
	}

	// initHadSchema records whether the host sent an outputSchema on initialize,
	// so the "structured" turn can prove the Go→TS schema wiring end to end.
	initHadSchema := false

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var frame struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Params struct {
				OutputSchema json.RawMessage `json:"outputSchema"`
			} `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			continue
		}
		// The host's reply to our can_use_tool request (id, result, no method)
		// finishes the approval turn, echoing the decision so the test can verify
		// the round-trip reached the agent.
		if frame.Method == "" && len(frame.Result) > 0 {
			completed("decision=" + string(frame.Result))
			continue
		}
		switch frame.Method {
		case "initialize":
			initHadSchema = len(frame.Params.OutputSchema) > 0
			enc(map[string]any{"jsonrpc": "2.0", "id": id(frame.ID), "result": map[string]any{"ok": true}})
			enc(map[string]any{"jsonrpc": "2.0", "method": "session/init", "params": map[string]any{
				"session_id": "fake-sess", "model": "claude-sonnet-4-5", "tools": []string{"Read", "Bash"},
			}})
		case "prompt":
			enc(map[string]any{"jsonrpc": "2.0", "id": id(frame.ID), "result": map[string]any{"accepted": true}})
			enc(map[string]any{"jsonrpc": "2.0", "method": "message/text", "params": map[string]any{"text": "hi from fake"}})
			switch mode {
			case "approval":
				// Ask the host to vet a Bash tool use; the turn completes when the
				// host replies (handled above).
				enc(map[string]any{"jsonrpc": "2.0", "id": "perm-1", "method": "can_use_tool", "params": map[string]any{
					"tool": "Bash", "input": map[string]any{"command": "ls"}, "tool_use_id": "tu1",
				}})
			case "hang":
				// Emit nothing more; wait for the interrupt control request.
			case "structured":
				// Complete with a structured_output payload, echoing whether the
				// host actually transmitted the schema on initialize.
				enc(map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{
					"success": true, "session_id": "fake-sess", "cost_usd": 0.01, "subtype": "success",
					"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
					"structured_output": map[string]any{
						"company_name":    "Anthropic",
						"founded_year":    2021,
						"received_schema": initHadSchema,
					},
				}})
			default:
				enc(map[string]any{"jsonrpc": "2.0", "method": "message/tool_use", "params": map[string]any{
					"tool": "Read", "id": "t1", "input": map[string]any{"file_path": "/x"},
				}})
				completed("hi from fake")
			}
		case "interrupt":
			if marker != "" {
				_ = os.WriteFile(marker, []byte("interrupted"), 0o644)
			}
			enc(map[string]any{"jsonrpc": "2.0", "id": id(frame.ID), "result": map[string]any{}})
		case "shutdown":
			enc(map[string]any{"jsonrpc": "2.0", "id": id(frame.ID), "result": map[string]any{}})
			os.Exit(0)
		}
	}
}

func withFakeAgentProcess(t *testing.T) {
	t.Helper()
	withFakeAgentProcessEnv(t, map[string]string{fakeServerEnv: "1"})
}

func withFakeAgentProcessEnv(t *testing.T, env map[string]string) {
	t.Helper()
	self, err := os.Executable()
	require.NoError(t, err)

	orig := newAgentProcess
	newAgentProcess = func(*Provider) (*exec.Process, error) {
		return exec.NewExec(self).
			WithStdioPipe().
			WithEnv(env), nil
	}
	t.Cleanup(func() { newAgentProcess = orig })
}

func TestProvider_StreamLifecycle(t *testing.T) {
	withFakeAgentProcess(t)

	p, err := New(ai.Config{Model: api.Model{Name: "claude-agent-sonnet"}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })

	assert.Equal(t, ai.BackendClaudeAgent, p.GetBackend())
	assert.Equal(t, "claude-agent-sonnet", p.GetModel())

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

	p, err := New(ai.Config{Model: api.Model{Name: "claude-agent-sonnet"}})
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

	p, err := New(ai.Config{Model: api.Model{Name: "claude-agent-sonnet"}})
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

		p := &Provider{model: "claude-agent-sonnet", sessionSchema: raw}
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

	t.Run("non-struct target fails loudly", func(t *testing.T) {
		_, err := requestSchemaJSON(ai.Request{Prompt: api.Prompt{Schema: "not a struct"}})
		require.Error(t, err)
	})
}

func TestProvider_MultiTurnSerialized(t *testing.T) {
	withFakeAgentProcess(t)

	p, err := New(ai.Config{Model: api.Model{Name: "claude-agent-sonnet"}})
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
	p, err := New(ai.Config{Model: api.Model{Name: "claude-agent-sonnet"}, CanUseTool: canUseTool})
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

	p, err := New(ai.Config{Model: api.Model{Name: "claude-agent-sonnet"}})
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
