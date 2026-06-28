package claudeagent

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/clicky/exec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeServerEnv switches the test binary into a fake JSON-RPC agent server when
// re-exec'd by the supervised process, so the provider lifecycle is exercised
// without npm, tsx or a real claude binary.
const fakeServerEnv = "CLAUDEAGENT_FAKE_SERVER"

func TestMain(m *testing.M) {
	if os.Getenv(fakeServerEnv) == "1" {
		runFakeServer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runFakeServer speaks the agent.ts JSON-RPC protocol over stdio: it answers
// initialize/prompt/interrupt/shutdown and pushes a scripted set of turn
// notifications so the Go provider has a realistic stream to map.
func runFakeServer() {
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

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			enc(map[string]any{"jsonrpc": "2.0", "id": id(req.ID), "result": map[string]any{"ok": true}})
			enc(map[string]any{"jsonrpc": "2.0", "method": "session/init", "params": map[string]any{
				"session_id": "fake-sess", "model": "claude-sonnet-4-5", "tools": []string{"Read", "Bash"},
			}})
		case "prompt":
			enc(map[string]any{"jsonrpc": "2.0", "id": id(req.ID), "result": map[string]any{"accepted": true}})
			enc(map[string]any{"jsonrpc": "2.0", "method": "message/text", "params": map[string]any{"text": "hi from fake"}})
			enc(map[string]any{"jsonrpc": "2.0", "method": "message/tool_use", "params": map[string]any{
				"tool": "Read", "id": "t1", "input": map[string]any{"file_path": "/x"},
			}})
			enc(map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{
				"success": true, "session_id": "fake-sess", "cost_usd": 0.01,
				"result_text": "hi from fake",
				"usage":       map[string]any{"input_tokens": 10, "output_tokens": 5},
			}})
		case "interrupt":
			enc(map[string]any{"jsonrpc": "2.0", "id": id(req.ID), "result": map[string]any{}})
		case "shutdown":
			enc(map[string]any{"jsonrpc": "2.0", "id": id(req.ID), "result": map[string]any{}})
			os.Exit(0)
		}
	}
}

func withFakeAgentProcess(t *testing.T) {
	t.Helper()
	self, err := os.Executable()
	require.NoError(t, err)

	orig := newAgentProcess
	newAgentProcess = func(*Provider) (*exec.Process, error) {
		return exec.NewExec(self).
			WithStdioPipe().
			WithEnv(map[string]string{fakeServerEnv: "1"}), nil
	}
	t.Cleanup(func() { newAgentProcess = orig })
}

func TestProvider_StreamLifecycle(t *testing.T) {
	withFakeAgentProcess(t)

	p, err := New(ai.Config{Model: "claude-agent-sonnet"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })

	assert.Equal(t, ai.BackendClaudeAgent, p.GetBackend())
	assert.Equal(t, "claude-agent-sonnet", p.GetModel())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	events, err := p.ExecuteStream(ctx, ai.Request{Prompt: "hello"})
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

	p, err := New(ai.Config{Model: "claude-agent-sonnet"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := p.Execute(ctx, ai.Request{Prompt: "hello"})
	require.NoError(t, err)
	assert.Equal(t, "hi from fake", resp.Text)
	assert.Equal(t, ai.BackendClaudeAgent, resp.Backend)
	assert.Equal(t, 10, resp.Usage.InputTokens)
}

func TestProvider_MultiTurnSerialized(t *testing.T) {
	withFakeAgentProcess(t)

	p, err := New(ai.Config{Model: "claude-agent-sonnet"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Two sequential turns over the same supervised session must both complete.
	for turn := 0; turn < 2; turn++ {
		events, err := p.ExecuteStream(ctx, ai.Request{Prompt: "go"})
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
