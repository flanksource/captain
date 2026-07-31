package claudeagent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/flanksource/clicky/exec"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

const (
	fakeServerEnv = "CLAUDEAGENT_FAKE_SERVER"
	fakeModeEnv   = "CLAUDEAGENT_FAKE_MODE"
	fakeMarkerEnv = "CLAUDEAGENT_FAKE_MARKER"
)

type fakeCallerToolServer struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

type fakeInitializeParams struct {
	OutputSchema       json.RawMessage                 `json:"outputSchema"`
	MCPServers         map[string]fakeCallerToolServer `json:"mcpServers"`
	CallerToolUseIDKey string                          `json:"callerToolUseIDKey"`
}

func TestMain(m *testing.M) {
	if os.Getenv(fakeServerEnv) == "1" {
		runFakeServer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runFakeServer() {
	mode := os.Getenv(fakeModeEnv)
	marker := os.Getenv(fakeMarkerEnv)
	var initialization fakeInitializeParams
	initHadSchema := false
	promptCount := 0

	enc := func(obj map[string]any) {
		encoded, _ := json.Marshal(obj)
		_, _ = os.Stdout.Write(append(encoded, '\n'))
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

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var frame struct {
			ID     json.RawMessage      `json:"id"`
			Method string               `json:"method"`
			Result json.RawMessage      `json:"result"`
			Params fakeInitializeParams `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &frame) != nil {
			continue
		}
		if frame.Method == "" && len(frame.Result) > 0 {
			completed("decision=" + string(frame.Result))
			continue
		}
		switch frame.Method {
		case "initialize":
			initialization = frame.Params
			initHadSchema = len(frame.Params.OutputSchema) > 0
			enc(map[string]any{"jsonrpc": "2.0", "id": id(frame.ID), "result": map[string]any{"ok": true}})
			enc(map[string]any{"jsonrpc": "2.0", "method": "session/init", "params": map[string]any{
				"session_id": "fake-sess", "model": "claude-sonnet-4-5", "tools": []string{"Read", "Bash"},
			}})
		case "prompt":
			promptCount++
			enc(map[string]any{"jsonrpc": "2.0", "id": id(frame.ID), "result": map[string]any{"accepted": true}})
			enc(map[string]any{"jsonrpc": "2.0", "method": "message/text", "params": map[string]any{"text": "hi from fake"}})
			runFakeTurn(mode, promptCount, initHadSchema, initialization, enc, completed)
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

func runFakeTurn(
	mode string,
	promptCount int,
	initHadSchema bool,
	initialization fakeInitializeParams,
	enc func(map[string]any),
	completed func(string),
) {
	switch mode {
	case "error-output":
		_, _ = os.Stderr.WriteString("claude subprocess authentication detail\n")
		enc(map[string]any{"jsonrpc": "2.0", "method": "turn/error", "params": map[string]any{
			"message": "Claude Code process exited with code 1",
		}})
	case "steer":
		if promptCount == 2 {
			completed("first prompt complete")
			completed("steered prompt complete")
		}
	case "approval":
		enc(map[string]any{"jsonrpc": "2.0", "id": "perm-1", "method": "can_use_tool", "params": map[string]any{
			"tool": "Bash", "input": map[string]any{"command": "ls"}, "tool_use_id": "tu1",
		}})
	case "plan-approval":
		enc(map[string]any{"jsonrpc": "2.0", "method": "message/tool_use", "params": map[string]any{
			"tool": "ExitPlanMode", "id": "tu-plan",
			"input": map[string]any{"plan": "1. change the seam", "planFilePath": "/repo/.claude/plans/x.md"},
		}})
		enc(map[string]any{"jsonrpc": "2.0", "id": "perm-plan", "method": "can_use_tool", "params": map[string]any{
			"tool": "ExitPlanMode", "tool_use_id": "tu-plan",
			"input": map[string]any{"plan": "1. change the seam", "planFilePath": "/repo/.claude/plans/x.md"},
		}})
	case "hang":
	case "structured":
		enc(map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{
			"success": true, "session_id": "fake-sess", "cost_usd": 0.01, "subtype": "success",
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
			"structured_output": map[string]any{
				"company_name": "Anthropic", "founded_year": 2021, "received_schema": initHadSchema,
			},
		}})
	case "caller-tools":
		if err := runFakeCallerTool(initialization, enc, completed); err != nil {
			enc(map[string]any{"jsonrpc": "2.0", "method": "turn/error", "params": map[string]any{"message": err.Error()}})
		}
	default:
		enc(map[string]any{"jsonrpc": "2.0", "method": "message/tool_use", "params": map[string]any{
			"tool": "Read", "id": "t1", "input": map[string]any{"file_path": "/x"},
		}})
		completed("hi from fake")
	}
}

func runFakeCallerTool(
	initialization fakeInitializeParams,
	enc func(map[string]any),
	completed func(string),
) error {
	server, ok := initialization.MCPServers["captain"]
	if !ok || initialization.CallerToolUseIDKey == "" {
		return fmt.Errorf("fake caller tools were not initialized")
	}
	const toolUseID = "claude-tool-use-1"
	enc(map[string]any{"jsonrpc": "2.0", "method": "message/tool_use", "params": map[string]any{
		"tool": "invoice_get", "id": toolUseID, "input": map[string]any{"id": "inv-1"},
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	channel, err := transport.NewStreamableHTTP(server.URL, transport.WithHTTPHeaders(server.Headers))
	if err != nil {
		return err
	}
	client := mcpclient.NewClient(channel)
	if err := client.Start(ctx); err != nil {
		return err
	}
	defer client.Close()
	request := mcp.InitializeRequest{}
	request.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	request.Params.ClientInfo = mcp.Implementation{Name: "claude-agent-fake", Version: "1.0.0"}
	if _, err := client.Initialize(ctx, request); err != nil {
		return err
	}
	call := mcp.CallToolRequest{}
	call.Params.Name = "invoice_get"
	call.Params.Arguments = map[string]any{
		"id": "inv-1", initialization.CallerToolUseIDKey: toolUseID,
	}
	result, err := client.CallTool(ctx, call)
	if err != nil {
		return err
	}
	content, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return err
	}
	enc(map[string]any{"jsonrpc": "2.0", "method": "message/tool_result", "params": map[string]any{
		"id": toolUseID, "content": string(content), "is_error": result.IsError,
	}})
	completed("caller tool complete")
	return nil
}

func withFakeAgentProcess(t *testing.T) {
	t.Helper()
	withFakeAgentProcessEnv(t, map[string]string{fakeServerEnv: "1"})
}

func withFakeAgentProcessEnv(t *testing.T, env map[string]string) {
	t.Helper()
	self, err := os.Executable()
	require.NoError(t, err)
	original := newAgentProcess
	newAgentProcess = func(*Provider) (*exec.Process, error) {
		return exec.NewExec(self).WithStdioPipe().WithEnv(env), nil
	}
	t.Cleanup(func() { newAgentProcess = original })
}
