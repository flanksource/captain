package provider

import (
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
)

func TestBuildCodexExecArgs_BasicShape(t *testing.T) {
	args, err := buildCodexExecArgs("o3", ai.Request{Prompt: "hello"})
	if err != nil {
		t.Fatalf("buildCodexExecArgs: %v", err)
	}
	if args[0] != "exec" {
		t.Errorf("args[0] = %q, want \"exec\"", args[0])
	}
	have := strings.Join(args, " ")
	for _, want := range []string{"exec", "--json", "--skip-git-repo-check", "--model o3", "hello"} {
		if !strings.Contains(have, want) {
			t.Errorf("args missing %q\n got: %s", want, have)
		}
	}
	if args[len(args)-1] != "hello" {
		t.Errorf("last arg = %q, want prompt", args[len(args)-1])
	}
}

func TestBuildCodexExecArgs_FlagTranslation(t *testing.T) {
	tests := []struct {
		name        string
		req         ai.Request
		mustContain []string
		mustNot     []string
	}{
		{
			name:        "edit maps to workspace-write sandbox",
			req:         ai.Request{Prompt: "p", Edit: true},
			mustContain: []string{"--sandbox workspace-write"},
		},
		{
			name:        "explicit permission mode skips sandbox default",
			req:         ai.Request{Prompt: "p", Edit: true, PermissionMode: "default"},
			mustNot:     []string{"--sandbox workspace-write"},
		},
		{
			name:        "bypassPermissions maps to dangerously-bypass",
			req:         ai.Request{Prompt: "p", PermissionMode: "bypassPermissions"},
			mustContain: []string{"--dangerously-bypass-approvals-and-sandbox"},
		},
		{
			name:        "no-mcp passes -c override",
			req:         ai.Request{Prompt: "p", NoMCP: true},
			mustContain: []string{"-c mcp_servers={}"},
		},
		{
			name:        "no-user maps to ignore-user-config",
			req:         ai.Request{Prompt: "p", NoUser: true},
			mustContain: []string{"--ignore-user-config"},
			mustNot:     []string{"--ignore-rules", "--ephemeral"},
		},
		{
			name:        "no-project maps to ignore-rules",
			req:         ai.Request{Prompt: "p", NoProject: true},
			mustContain: []string{"--ignore-rules"},
		},
		{
			name:        "no-memory maps to ephemeral",
			req:         ai.Request{Prompt: "p", NoMemory: true},
			mustContain: []string{"--ephemeral"},
		},
		{
			name: "bare composes the no-* flags",
			req:  ai.Request{Prompt: "p", Bare: true},
			mustContain: []string{
				"--ignore-user-config",
				"--ignore-rules",
				"--ephemeral",
			},
		},
		{
			name:        "system prompt is prepended to the user prompt",
			req:         ai.Request{Prompt: "task", SystemPrompt: "be brief"},
			mustContain: []string{"be brief\n\ntask"},
		},
		{
			name:        "append-system-prompt is appended",
			req:         ai.Request{Prompt: "task", AppendSystemPrompt: "tail rule"},
			mustContain: []string{"task\n\ntail rule"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args, err := buildCodexExecArgs("gpt-5", tc.req)
			if err != nil {
				t.Fatalf("buildCodexExecArgs: %v", err)
			}
			have := strings.Join(args, " ")
			for _, want := range tc.mustContain {
				if !strings.Contains(have, want) {
					t.Errorf("args missing %q\n got: %s", want, have)
				}
			}
			for _, forbidden := range tc.mustNot {
				if strings.Contains(have, forbidden) {
					t.Errorf("args contain forbidden %q\n got: %s", forbidden, have)
				}
			}
		})
	}
}

func TestMapCodexEvent(t *testing.T) {
	tests := []struct {
		name string
		line string
		want ai.EventKind
	}{
		{
			name: "agent_message",
			line: `{"type":"agent_message","msg":{"type":"agent_message","message":"hello"}}`,
			want: ai.EventText,
		},
		{
			name: "agent_message_delta",
			line: `{"msg":{"type":"agent_message_delta","delta":"chunk"}}`,
			want: ai.EventText,
		},
		{
			name: "tool_call",
			line: `{"msg":{"type":"tool_call","name":"shell","input":{"cmd":"ls"}}}`,
			want: ai.EventToolUse,
		},
		{
			name: "task_complete carries usage and cost",
			line: `{"msg":{"type":"task_complete","cost_usd":0.0123,"usage":{"input_tokens":42,"output_tokens":17}}}`,
			want: ai.EventResult,
		},
		{
			name: "error",
			line: `{"msg":{"type":"error","error":"boom"}}`,
			want: ai.EventError,
		},
		{
			name: "session_configured",
			line: `{"id":"sess-1","msg":{"type":"session_configured"}}`,
			want: ai.EventSystem,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev, ok := mapCodexEvent(tc.line, "gpt-5")
			if !ok {
				t.Fatalf("mapCodexEvent dropped line: %s", tc.line)
			}
			if ev.Kind != tc.want {
				t.Errorf("kind = %q, want %q", ev.Kind, tc.want)
			}
		})
	}

	if _, ok := mapCodexEvent(`{"msg":{"type":"heartbeat"}}`, "m"); ok {
		t.Errorf("heartbeat events should be dropped")
	}
	if _, ok := mapCodexEvent(`not json`, "m"); ok {
		t.Errorf("non-json should be dropped")
	}
}

func TestMapCodexEvent_TaskCompleteUsage(t *testing.T) {
	ev, ok := mapCodexEvent(`{"msg":{"type":"task_complete","cost_usd":0.5,"usage":{"input_tokens":100,"output_tokens":50}}}`, "gpt-5")
	if !ok {
		t.Fatal("expected event")
	}
	if ev.CostUSD != 0.5 {
		t.Errorf("cost = %v, want 0.5", ev.CostUSD)
	}
	if ev.Usage == nil || ev.Usage.InputTokens != 100 || ev.Usage.OutputTokens != 50 {
		t.Errorf("usage = %+v, want input=100 output=50", ev.Usage)
	}
	if !ev.Success {
		t.Error("expected Success=true when no error present")
	}
}
