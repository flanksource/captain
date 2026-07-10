package provider

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	history "github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/shell"
)

func TestBuildCodexCLIArgs(t *testing.T) {
	cwd := t.TempDir()
	req := ai.Request{
		Model: api.Model{Effort: api.EffortUltra},
		Prompt: api.Prompt{
			User:       "hello",
			SchemaJSON: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}}}`),
		},
		Setup:     &shell.Setup{Cwd: cwd},
		SessionID: "thread-1",
		Memory: api.Memory{
			SkipMemory:  true,
			SkipUser:    true,
			SkipProject: true,
			SkipHooks:   true,
		},
		Permissions: api.Permissions{Presets: []api.Preset{api.PresetEdit}},
	}

	args, cleanup, err := buildCodexCLIArgs("gpt-5.5", req)
	if err != nil {
		t.Fatalf("buildCodexCLIArgs: %v", err)
	}
	defer cleanup()
	requireFlagValue(t, args, "-m", "gpt-5.5")
	requireFlagValue(t, args, "-c", `model_reasoning_effort="ultra"`)
	requireFlagValue(t, args, "-C", cwd)
	requireFlagValue(t, args, "--sandbox", "workspace-write")
	requireHasArg(t, args, "--json")
	requireHasArg(t, args, "--ephemeral")
	requireHasArg(t, args, "--ignore-user-config")
	requireHasArg(t, args, "--ignore-rules")
	if got := args[len(args)-2:]; got[0] != "resume" || got[1] != "thread-1" {
		t.Fatalf("args suffix = %v, want resume thread-1 (args: %v)", got, args)
	}
	schemaPath := flagValue(t, args, "--output-schema")
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema file: %v", err)
	}
	if !json.Valid(data) {
		t.Fatalf("schema file is not valid JSON: %s", data)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("decode schema file: %v", err)
	}
	if got := schema["required"]; !reflect.DeepEqual(got, []any{"answer"}) {
		t.Fatalf("schema required = %#v, want [answer]", got)
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("schema additionalProperties = %v, want false", schema["additionalProperties"])
	}
	cleanup()
	if _, err := os.Stat(schemaPath); !os.IsNotExist(err) {
		t.Fatalf("schema cleanup stat err = %v, want not exist", err)
	}
}

func TestCodexCLIStateMapsJSONLEvents(t *testing.T) {
	state := codexCLIState{model: "gpt-5.5", pending: map[string]history.CodexEvent{}}

	events := state.mapLine([]byte(`{"type":"thread.started","thread_id":"thread-1"}`))
	if len(events) != 1 || events[0].Kind != ai.EventSystem || events[0].SessionID != "thread-1" {
		t.Fatalf("thread.started events = %+v", events)
	}

	events = state.mapLine([]byte(`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}}`))
	if len(events) != 1 || events[0].Kind != ai.EventText || events[0].Text != "hello" {
		t.Fatalf("message events = %+v", events)
	}

	events = state.mapLine([]byte(`{"type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"call-1","arguments":"{\"command\":\"pwd\"}"}}`))
	if len(events) != 0 {
		t.Fatalf("function_call events = %+v, want none until output", events)
	}
	events = state.mapLine([]byte(`{"type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"ok"}}`))
	if len(events) != 1 || events[0].Kind != ai.EventToolUse || events[0].Tool != "shell" {
		t.Fatalf("function_call_output events = %+v", events)
	}
	if got, _ := events[0].Input["command"].(string); got != "pwd" {
		t.Fatalf("tool input command = %q, want pwd", got)
	}

	events = state.mapLine([]byte(`{"type":"turn.completed","usage":{"input_tokens":3,"output_tokens":2}}`))
	if len(events) != 1 || events[0].Kind != ai.EventResult || !events[0].Success {
		t.Fatalf("turn.completed events = %+v", events)
	}
	if events[0].Usage == nil || events[0].Usage.InputTokens != 3 || events[0].Usage.OutputTokens != 2 {
		t.Fatalf("usage = %+v", events[0].Usage)
	}
}
