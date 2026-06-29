package cmux

import (
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/api"
)

func TestAgentCommand(t *testing.T) {
	cases := []struct {
		name string
		opts AgentCommandOpts
		want string
	}{
		{"claude fresh session", AgentCommandOpts{Agent: "claude", SessionID: "s1"}, "claude --session-id s1"},
		{"claude resume", AgentCommandOpts{Agent: "claude", SessionID: "s1", Resume: true}, "claude --resume s1"},
		{"claude plan forces plan mode", AgentCommandOpts{Agent: "claude", SessionID: "s1", Plan: true}, "claude --session-id s1 --permission-mode plan"},
		{"claude acceptEdits mode", AgentCommandOpts{Agent: "claude", PermissionMode: api.PermissionAcceptEdits}, "claude --permission-mode acceptEdits"},
		{"claude bypass mode", AgentCommandOpts{Agent: "claude", PermissionMode: api.PermissionBypass}, "claude --permission-mode bypassPermissions"},
		{"claude allow + deny tools", AgentCommandOpts{Agent: "claude", AllowedTools: []string{"Read", "Edit"}, DisallowedTools: []string{"Bash"}}, "claude --allowedTools Read,Edit --disallowedTools Bash"},
		{"claude model flag", AgentCommandOpts{Agent: "claude", Model: "opus"}, "claude --model opus"},
		{"claude no permission flag when unset", AgentCommandOpts{Agent: "claude", SessionID: "s"}, "claude --session-id s"},
		{"claude full", AgentCommandOpts{Agent: "claude", SessionID: "s", PermissionMode: api.PermissionAcceptEdits, AllowedTools: []string{"Read"}, Model: "opus"}, "claude --session-id s --permission-mode acceptEdits --allowedTools Read --model opus"},
		{"codex bare", AgentCommandOpts{Agent: "codex"}, "codex"},
		{"codex with model", AgentCommandOpts{Agent: "codex", Model: "gpt-5"}, "codex -m gpt-5"},
		{"codex ignores tools and permission", AgentCommandOpts{Agent: "codex", AllowedTools: []string{"Read"}, PermissionMode: api.PermissionPlan}, "codex"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AgentCommand(tc.opts); got != tc.want {
				t.Fatalf("AgentCommand() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCLIPermissionMode(t *testing.T) {
	cases := map[api.PermissionMode]string{
		api.PermissionPlan:        "plan",
		api.PermissionAcceptEdits: "acceptEdits",
		api.PermissionBypass:      "bypassPermissions",
		api.PermissionDefault:     "default",
		api.PermissionAuto:        "default",
		"":                        "default",
	}
	for mode, want := range cases {
		if got := cliPermissionMode(mode); got != want {
			t.Errorf("cliPermissionMode(%q) = %q, want %q", mode, got, want)
		}
	}
}

func TestWithEnvSortsAndQuotes(t *testing.T) {
	got := withEnv("claude", map[string]string{"B": "2", "A": "o'clock"})
	want := `A='o'\''clock' B='2' claude`
	if got != want {
		t.Fatalf("withEnv() = %q, want %q", got, want)
	}
	if unchanged := withEnv("claude", nil); unchanged != "claude" {
		t.Fatalf("withEnv(nil) = %q, want unchanged", unchanged)
	}
}

func TestModelFlag(t *testing.T) {
	cases := []struct {
		agent, model, want string
	}{
		{"claude", "claude", ""},
		{"claude", "", ""},
		{"claude", "opus", "opus"},
		{"codex", "codex", ""},
		{"codex", "gpt-5", "gpt-5"},
	}
	for _, tc := range cases {
		if got := modelFlag(tc.agent, tc.model); got != tc.want {
			t.Errorf("modelFlag(%q,%q) = %q, want %q", tc.agent, tc.model, got, tc.want)
		}
	}
}

func TestGroupWorkDir(t *testing.T) {
	cases := map[string]string{
		"":         ".",
		"/a/b/":    "/a/b",
		"x/./y":    "x/y",
		"/repo//s": "/repo/s",
	}
	for in, want := range cases {
		if got := groupWorkDir(in); got != want {
			t.Errorf("groupWorkDir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAgentWorkspaceName(t *testing.T) {
	if got := AgentWorkspaceName("/repo", "claude"); got != "repo-claude" {
		t.Fatalf("AgentWorkspaceName(/repo, claude) = %q, want repo-claude", got)
	}
	if got := AgentWorkspaceName("/repo", ""); got != "repo" {
		t.Fatalf("AgentWorkspaceName(/repo, \"\") = %q, want repo", got)
	}
}

func TestTruncatePrompt(t *testing.T) {
	if body, truncated := truncatePrompt("short", 100); truncated || body != "short" {
		t.Fatalf("truncatePrompt(short) = (%q, %v), want (short, false)", body, truncated)
	}
	long := strings.Repeat("line\n", 100)
	body, truncated := truncatePrompt(long, 20)
	if !truncated {
		t.Fatal("truncatePrompt(long) truncated = false, want true")
	}
	if len(body) > 20 {
		t.Fatalf("truncated body = %d bytes, want <= 20", len(body))
	}
}
