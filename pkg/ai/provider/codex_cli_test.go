package provider

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
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

	args, cleanup, err := buildCodexCLIArgs(codexCLIConfig{Model: "gpt-5.5"}, req)
	if err != nil {
		t.Fatalf("buildCodexCLIArgs: %v", err)
	}
	defer cleanup()
	requireFlagValue(t, args, "-m", "gpt-5.5")
	requireFlagValue(t, args, "-c", `model_reasoning_effort="ultra"`)
	if effort, present := codexCLIReasoningEffort(args); !present || effort != "ultra" {
		t.Fatalf("native reasoning effort = %q, %t; want ultra, true", effort, present)
	}
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

// TestBuildCodexCLIArgsEmitsApprovalPolicy pins the other half of CodexSafety.
// `codex exec` has no --ask-for-approval flag (verified against codex-cli
// 0.147.0), so the approval policy the shared helper computes has to ride on
// -c approval_policy — dropping it left the exec path enforcing only half the
// posture the app-server path enforced from the same helper.
func TestBuildCodexCLIArgsEmitsApprovalPolicy(t *testing.T) {
	tests := []struct {
		name         string
		permissions  api.Permissions
		wantSandbox  string
		wantApproval string
	}{
		{
			name:         "default is read-only and still asks",
			wantSandbox:  "read-only",
			wantApproval: `approval_policy="on-request"`,
		},
		{
			name:         "edit widens the sandbox but keeps asking",
			permissions:  api.Permissions{Presets: []api.Preset{api.PresetEdit}},
			wantSandbox:  "workspace-write",
			wantApproval: `approval_policy="on-request"`,
		},
		{
			name:         "bypass grants full access and stops asking",
			permissions:  api.Permissions{Mode: api.PermissionBypass},
			wantSandbox:  "danger-full-access",
			wantApproval: `approval_policy="never"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, cleanup, err := buildCodexCLIArgs(
				codexCLIConfig{Model: "gpt-5.5"},
				ai.Request{Prompt: api.Prompt{User: "hi"}, Permissions: tt.permissions},
			)
			if err != nil {
				t.Fatalf("buildCodexCLIArgs: %v", err)
			}
			defer cleanup()
			requireFlagValue(t, args, "--sandbox", tt.wantSandbox)
			requireFlagPair(t, args, "-c", tt.wantApproval)
			for _, arg := range args {
				if arg == "--ask-for-approval" {
					t.Fatalf("emitted --ask-for-approval, which codex exec does not accept: %v", args)
				}
			}
		})
	}
}

// codex ignores OPENAI_BASE_URL once an account credential is stored, so the
// override has to be declared as a model provider on the command line.
func TestBuildCodexCLIArgsRedirectsViaModelProvider(t *testing.T) {
	const baseURL = "http://127.0.0.1:9999/v1"
	req := ai.Request{Prompt: api.Prompt{User: "hello"}}

	args, cleanup, err := buildCodexCLIArgs(codexCLIConfig{Model: "gpt-5.5", APIURL: baseURL}, req)
	if err != nil {
		t.Fatalf("buildCodexCLIArgs: %v", err)
	}
	defer cleanup()
	want := []string{
		"-c", "model_provider=captain",
		"-c", "model_providers.captain.name=captain",
		"-c", "model_providers.captain.base_url=" + baseURL,
		"-c", "model_providers.captain.env_key=OPENAI_API_KEY",
		"-c", "model_providers.captain.wire_api=responses",
	}
	if got := args[2 : 2+len(want)]; !reflect.DeepEqual(got, want) {
		t.Fatalf("override args = %v, want %v (args: %v)", got, want, args)
	}

	plain, cleanupPlain, err := buildCodexCLIArgs(codexCLIConfig{Model: "gpt-5.5"}, req)
	if err != nil {
		t.Fatalf("buildCodexCLIArgs without APIURL: %v", err)
	}
	defer cleanupPlain()
	for _, arg := range plain {
		if strings.HasPrefix(arg, "model_provider") {
			t.Fatalf("no APIURL must leave the provider alone, got args %v", plain)
		}
	}
}

func TestNewCodexCLICarriesAPIURL(t *testing.T) {
	const baseURL = "http://127.0.0.1:9999/v1"
	got := NewCodexCLI(ai.Config{Model: api.Model{Name: "gpt-5.5"}, APIURL: " " + baseURL + " "})
	if got.apiURL != baseURL {
		t.Fatalf("apiURL = %q, want %q", got.apiURL, baseURL)
	}
	if got.model != "gpt-5.5" {
		t.Fatalf("model = %q, want gpt-5.5", got.model)
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
	if len(events) != 1 || events[0].Kind != ai.EventToolUse || events[0].Tool != "Bash" {
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

// A token_count event supplies cache/reasoning-aware totals; codex reports
// input_tokens inclusive of cache and output_tokens inclusive of reasoning, so
// the emitted usage must be netted to disjoint buckets (findings B1/B2) and the
// coarser turn.completed per-turn counts must not clobber it.
func TestCodexCLIStateNetsTokenCountUsage(t *testing.T) {
	state := codexCLIState{model: "gpt-5.5", pending: map[string]history.CodexEvent{}}

	events := state.mapLine([]byte(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":120,"cached_input_tokens":12,"output_tokens":40,"reasoning_output_tokens":7}}}}`))
	if len(events) != 0 {
		t.Fatalf("token_count should emit no events, got %+v", events)
	}

	events = state.mapLine([]byte(`{"type":"turn.completed","usage":{"input_tokens":999,"output_tokens":999}}`))
	if len(events) != 1 || events[0].Usage == nil {
		t.Fatalf("turn.completed events = %+v", events)
	}
	u := events[0].Usage
	if u.InputTokens != 108 || u.OutputTokens != 33 || u.CacheReadTokens != 12 || u.ReasoningTokens != 7 {
		t.Fatalf("usage = %+v, want input=108 output=33 cache=12 reasoning=7 (netted, not clobbered by turn.completed)", u)
	}
	if u.InputTokens+u.CacheReadTokens != 120 || u.OutputTokens+u.ReasoningTokens != 40 {
		t.Fatalf("disjoint buckets must recover raw totals: %+v", u)
	}
}

func TestCodexCLIStatePreservesUsagePresence(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		wantUsage bool
	}{
		{name: "omitted", line: `{"type":"turn.completed"}`, wantUsage: false},
		{name: "present zero", line: `{"type":"turn.completed","usage":{"input_tokens":0,"output_tokens":0}}`, wantUsage: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := codexCLIState{model: "gpt-5"}
			events := state.mapLine([]byte(test.line))
			if len(events) != 1 || events[0].Kind != ai.EventResult {
				t.Fatalf("turn.completed events = %+v", events)
			}
			if (events[0].Usage != nil) != test.wantUsage {
				t.Fatalf("usage = %#v, want present %t", events[0].Usage, test.wantUsage)
			}
			if events[0].Usage != nil && *events[0].Usage != (ai.Usage{}) {
				t.Fatalf("known-zero usage = %#v", events[0].Usage)
			}
		})
	}
}
