package cmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
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
		{"claude effort flag", AgentCommandOpts{Agent: "claude", Effort: api.EffortHigh}, "claude --effort high"},
		{"claude bare memory", AgentCommandOpts{Agent: "claude", Memory: api.Memory{Bare: true}}, "claude --bare"},
		{"claude skip skills", AgentCommandOpts{Agent: "claude", Memory: api.Memory{SkipSkills: true}}, "claude --disable-slash-commands"},
		{"claude skip project narrows sources", AgentCommandOpts{Agent: "claude", Memory: api.Memory{SkipProject: true}}, "claude --setting-sources user"},
		{"claude skip user narrows sources", AgentCommandOpts{Agent: "claude", Memory: api.Memory{SkipUser: true}}, "claude --setting-sources project,local"},
		{"claude dontAsk mode", AgentCommandOpts{Agent: "claude", PermissionMode: api.PermissionDontAsk}, "claude --permission-mode dontAsk"},
		{"claude extra args quote values", AgentCommandOpts{Agent: "claude", Extra: &api.ClaudeCmuxOptions{AddDir: []string{"pkg", "cmd"}, StrictMCPConfig: true, AppendSystem: "be terse"}}, "claude --add-dir pkg cmd --strict-mcp-config --append-system-prompt 'be terse'"},
		{"claude ignores codex extras", AgentCommandOpts{Agent: "claude", Extra: &api.CodexCmuxOptions{Search: true}}, "claude"},
		{"codex bare", AgentCommandOpts{Agent: "codex"}, "codex"},
		{"codex with model", AgentCommandOpts{Agent: "codex", Model: "gpt-5"}, "codex -m gpt-5"},
		{"codex ignores tools and permission", AgentCommandOpts{Agent: "codex", AllowedTools: []string{"Read"}, PermissionMode: api.PermissionPlan}, "codex"},
		{"codex extra posture and flags", AgentCommandOpts{Agent: "codex", Model: "gpt-5", Extra: &api.CodexCmuxOptions{Sandbox: api.CodexSandboxWorkspaceWrite, AskForApproval: api.CodexApprovalOnRequest, Search: true}}, "codex -m gpt-5 --sandbox workspace-write --ask-for-approval on-request --search"},
		{"codex ignores claude extras", AgentCommandOpts{Agent: "codex", Extra: &api.ClaudeCmuxOptions{AddDir: []string{"x"}}}, "codex"},
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
		api.PermissionAuto:        "auto",
		api.PermissionDontAsk:     "dontAsk",
		"":                        "default",
	}
	for mode, want := range cases {
		if got := cliPermissionMode(mode); got != want {
			t.Errorf("cliPermissionMode(%q) = %q, want %q", mode, got, want)
		}
	}
}

func TestCmuxExtraArgs(t *testing.T) {
	// codex seeds sandbox/approval from the permission posture when unset.
	seeded, err := cmuxExtraArgs("codex", ai.Request{Permissions: api.Permissions{Mode: api.PermissionBypass}})
	if err != nil {
		t.Fatalf("cmuxExtraArgs: %v", err)
	}
	codex, ok := seeded.(*api.CodexCmuxOptions)
	if !ok {
		t.Fatalf("got %T, want *api.CodexCmuxOptions", seeded)
	}
	if codex.Sandbox != api.CodexSandboxDangerFull || codex.AskForApproval != api.CodexApprovalNever {
		t.Errorf("seeded posture = (%q, %q), want (danger-full-access, never)", codex.Sandbox, codex.AskForApproval)
	}

	// An explicit CLIArgs sandbox overrides the seed; approval still seeds.
	override, err := cmuxExtraArgs("codex", ai.Request{
		Permissions: api.Permissions{Mode: api.PermissionBypass},
		CLIArgs:     map[string]any{"sandbox": "read-only"},
	})
	if err != nil {
		t.Fatalf("cmuxExtraArgs override: %v", err)
	}
	codex = override.(*api.CodexCmuxOptions)
	if codex.Sandbox != api.CodexSandboxReadOnly {
		t.Errorf("override sandbox = %q, want read-only", codex.Sandbox)
	}
	if codex.AskForApproval != api.CodexApprovalNever {
		t.Errorf("seeded approval = %q, want never", codex.AskForApproval)
	}

	// claude decodes CLIArgs into the claude option struct.
	claudeAny, err := cmuxExtraArgs("claude", ai.Request{CLIArgs: map[string]any{"addDir": []any{"pkg"}, "strictMcpConfig": true}})
	if err != nil {
		t.Fatalf("cmuxExtraArgs claude: %v", err)
	}
	claude := claudeAny.(*api.ClaudeCmuxOptions)
	if len(claude.AddDir) != 1 || claude.AddDir[0] != "pkg" || !claude.StrictMCPConfig {
		t.Errorf("claude decode = %+v, want AddDir=[pkg] StrictMCPConfig=true", claude)
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
		{"claude", "opus", "claude-opus-4-8"},
		{"claude", "claude-agent-opus", "claude-opus-4-8"},
		{"claude", "claude-code-sonnet", "claude-sonnet-5"},
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

func TestPromptTitle(t *testing.T) {
	cases := map[string]string{
		"Fix the bug":              "Fix the bug",
		"# Heading\n\nbody":        "Heading",
		"\n\n## Cmux switch\nrest": "Cmux switch",
		"   ":                      "Task",
		"":                         "Task",
	}
	for in, want := range cases {
		if got := promptTitle(in); got != want {
			t.Errorf("promptTitle(%q) = %q, want %q", in, got, want)
		}
	}
	if got := promptTitle(strings.Repeat("a", 200)); len(got) != maxTitleBytes {
		t.Errorf("promptTitle(long) len = %d, want %d", len(got), maxTitleBytes)
	}
}

func TestBuildInstruction(t *testing.T) {
	dir := t.TempDir()
	r := &run{}
	prompt := "# Switch to input file\n\nDo the thing across many lines.\n"

	got, err := r.buildInstruction(dir, "sess-1", prompt)
	if err != nil {
		t.Fatalf("buildInstruction: %v", err)
	}

	path := filepath.Join(dir, ".gavel", "cmux", "prompt-sess-1.md")
	if want := "Switch to input file - See " + path + " for full details"; got != want {
		t.Fatalf("buildInstruction = %q, want %q", got, want)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read prompt file: %v", err)
	}
	if !strings.Contains(string(data), "Do the thing across many lines.") {
		t.Errorf("prompt file missing full body: %q", data)
	}
}
