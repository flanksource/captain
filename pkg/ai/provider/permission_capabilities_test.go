package provider

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

// The capability table in pkg/api claims to describe these providers. A
// declaration nobody checks is just a comment, so these tests drive the real arg
// builders for every posture and compare the argv against the declared Effects.
//
// They live here rather than beside the table because the builders are
// unexported and sit above pkg/api: the declaration is the leaf, the
// implementation is what has to agree with it.

// argvFor is the real argv one runtime produces for one posture.
func argvFor(t *testing.T, runtime api.Runtime, mode api.PermissionMode) []string {
	t.Helper()
	req := ai.Request{Prompt: api.Prompt{User: "hi"}}
	switch runtime {
	case api.RuntimeOf(api.Anthropic, api.ModeCLI):
		req.Sandbox = &api.SandboxRef{Mode: api.SandboxNative}
		req.Permissions.Mode = mode
		args, cleanup, err := buildClaudeCLIArgs("claude-sonnet-5", req)
		if err != nil {
			t.Fatalf("buildClaudeCLIArgs(%s): %v", mode, err)
		}
		t.Cleanup(cleanup)
		return args
	case api.RuntimeOf(api.Google, api.ModeCLI):
		req.Sandbox = &api.SandboxRef{Mode: api.SandboxDocker}
		req.Permissions.Mode = mode
		args, err := buildGeminiCLIArgs("gemini-3.5-flash", req)
		if err != nil {
			t.Fatalf("buildGeminiCLIArgs(%s): %v", mode, err)
		}
		return args
	case api.RuntimeOf(api.OpenAI, api.ModeCLI):
		support := api.PermissionCapabilitiesFor(runtime).ModeSupport(mode)
		req.Sandbox = &api.SandboxRef{Mode: api.SandboxNative}
		req.Permissions.Mode = mode
		if support.Effects.Sandbox == "workspace-write" {
			req.Sandbox.Policy = &api.NativeSandboxPolicy{
				Filesystem: &api.SandboxFilesystemPolicy{Access: api.SandboxFilesystemWorkspaceWrite},
			}
		}
		if support.Effects.Sandbox == "danger-full-access" {
			// The posture stays stated: turning isolation off no longer supplies it.
			req.Sandbox.Mode = api.SandboxOff
		}
		args, cleanup, err := buildCodexCLIArgs(codexCLIConfig{Model: "gpt-5.5"}, req)
		if err != nil {
			t.Fatalf("buildCodexCLIArgs(%s): %v", mode, err)
		}
		t.Cleanup(cleanup)
		return args
	default:
		t.Fatalf("no argv builder for runtime %s", runtime)
		return nil
	}
}

// containsFlag reports whether "--flag value" appears as adjacent argv entries.
// Matching the pair rather than the value alone keeps `plan` from matching a
// model name or a prompt.
func containsFlag(args []string, flag string) bool {
	parts := strings.SplitN(flag, " ", 2)
	for i, arg := range args {
		if arg != parts[0] {
			continue
		}
		if len(parts) == 1 {
			return true
		}
		if i+1 < len(args) && args[i+1] == parts[1] {
			return true
		}
	}
	return false
}

// TestDeclaredPostureMatchesArgv is the flag-shaped half of the proof: every
// posture the table declares native or approximated must put its declared Flag
// into argv, and a declared-empty Flag must leave the knob unset so the CLI's
// own default stands.
func TestDeclaredPostureMatchesArgv(t *testing.T) {
	for _, runtime := range []api.Runtime{api.RuntimeOf(api.Anthropic, api.ModeCLI), api.RuntimeOf(api.Google, api.ModeCLI)} {
		caps := api.PermissionCapabilitiesFor(runtime)
		knob := strings.Fields(caps.ModeSupport(api.PermissionPlan).Effects.Flag)[0]

		for _, mode := range api.AllPermissionModes() {
			t.Run(fmt.Sprintf("%s/%s", runtime, mode), func(t *testing.T) {
				support := caps.ModeSupport(mode)
				if !support.Honoured() {
					t.Skipf("%s declares %s as %s", runtime, mode, support.Kind)
				}
				args := argvFor(t, runtime, mode)
				if support.Effects.Flag == "" {
					if slices.Contains(args, knob) {
						t.Fatalf("%s declares no flag for %s but argv carries %s: %v", runtime, mode, knob, args)
					}
					return
				}
				if !containsFlag(args, support.Effects.Flag) {
					t.Fatalf("%s declares %q for %s, argv was %v", runtime, support.Effects.Flag, mode, args)
				}
			})
		}
	}
}

// TestDeclaredCodexPostureMatchesArgv proves that approval translates without
// changing the independently selected Native isolation tier.
func TestDeclaredCodexPostureMatchesArgv(t *testing.T) {
	caps := api.PermissionCapabilitiesFor(api.OpenAI.Runtime(api.ModeCLI))
	for _, mode := range api.AllPermissionModes() {
		t.Run(string(mode), func(t *testing.T) {
			support := caps.ModeSupport(mode)
			if !support.Honoured() {
				t.Skipf("codex-cli declares %s as %s", mode, support.Kind)
			}
			args := argvFor(t, api.RuntimeOf(api.OpenAI, api.ModeCLI), mode)
			if !containsFlag(args, "--sandbox read-only") {
				t.Fatalf("native isolation changed for approval %s: %v", mode, args)
			}
			if !containsFlag(args, fmt.Sprintf("-c approval_policy=%q", support.Effects.Approval)) {
				t.Fatalf("declared approval %q for %s, argv was %v", support.Effects.Approval, mode, args)
			}
		})
	}
}

func TestDeclaredUnsupportedCodexDontAskFailsLoudly(t *testing.T) {
	ref := &api.SandboxRef{Mode: api.SandboxNative}
	_, cleanup, err := buildCodexCLIArgs(codexCLIConfig{Model: "gpt-5.5"}, ai.Request{
		Sandbox: ref, Permissions: api.Permissions{Mode: api.PermissionDontAsk},
	})
	t.Cleanup(cleanup)
	if err == nil || !strings.Contains(err.Error(), "permissions.mode") {
		t.Fatalf("codex dontAsk error = %v, want an unsupported permission mode error", err)
	}
}

// TestDeclaredAgentToolPolicyMatchesArgv proves the agent-provenance row two
// ways: where the table says native, allow and deny reach their flags; where it
// says unsupported, the builder refuses outright rather than dropping the policy.
//
// The refusal half matters most. RequireToolPolicySupport is the one place
// captain already fails loud instead of silently ignoring a permission field, and
// this pins the table to that behaviour so the two cannot drift apart.
func TestDeclaredAgentToolPolicyMatchesArgv(t *testing.T) {
	perms := api.Permissions{Tools: api.Tools{"Bash": api.ToolPolicyDeny, "Read": api.ToolPolicyAllow}}
	req := ai.Request{Prompt: api.Prompt{User: "hi"}, Permissions: perms}
	cases := []struct {
		runtime api.Runtime
		argv    func() ([]string, error)
	}{
		{api.RuntimeOf(api.Anthropic, api.ModeCLI), func() ([]string, error) {
			args, cleanup, err := buildClaudeCLIArgs("claude-sonnet-5", req)
			if cleanup != nil {
				t.Cleanup(cleanup)
			}
			return args, err
		}},
		{api.RuntimeOf(api.Google, api.ModeCLI), func() ([]string, error) {
			return buildGeminiCLIArgs("gemini-3.5-flash", req)
		}},
		{api.RuntimeOf(api.OpenAI, api.ModeCLI), func() ([]string, error) {
			args, cleanup, err := buildCodexCLIArgs(codexCLIConfig{Model: "gpt-5.5"}, req)
			if cleanup != nil {
				t.Cleanup(cleanup)
			}
			return args, err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.runtime.String(), func(t *testing.T) {
			caps := api.PermissionCapabilitiesFor(tc.runtime)
			enforces := caps.ToolPolicySupport(api.ProvenanceAgent, api.ToolPolicyDeny).Kind == api.SupportNative
			args, err := tc.argv()

			if !enforces {
				if err == nil {
					t.Fatalf("%s declares agent tool policy unsupported, but the run was accepted: %v", tc.runtime, args)
				}
				if !strings.Contains(err.Error(), tc.runtime.String()) {
					t.Fatalf("refusal should name the runtime, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s declares agent tool policy native but refused: %v", tc.runtime, err)
			}
			for _, pair := range []struct {
				policy api.ToolPolicy
				flag   string
			}{
				{api.ToolPolicyDeny, "--disallowedTools"},
				{api.ToolPolicyAllow, "--allowedTools"},
			} {
				declared := caps.ToolPolicySupport(api.ProvenanceAgent, pair.policy).Kind == api.SupportNative
				if got := slices.Contains(args, pair.flag); got != declared {
					t.Fatalf("declared %s/%s native=%v but argv carries %s = %v: %v",
						tc.runtime, pair.policy, declared, pair.flag, got, args)
				}
			}
		})
	}
}

// TestDeclaredMCPDisableMatchesArgv proves the resource row for claude-cli: the
// only backend whose argv genuinely silences ambient MCP servers is the only one
// declaring ResourceKindMCP/disabled as native.
func TestDeclaredMCPDisableMatchesArgv(t *testing.T) {
	req := ai.Request{Prompt: api.Prompt{User: "hi"}, Permissions: api.Permissions{MCP: api.MCP{Disabled: true}}}
	args, cleanup, err := buildClaudeCLIArgs("claude-sonnet-5", req)
	if err != nil {
		t.Fatalf("buildClaudeCLIArgs: %v", err)
	}
	t.Cleanup(cleanup)

	declared := api.PermissionCapabilitiesFor(api.Anthropic.Runtime(api.ModeCLI)).
		ResourceSupport(api.ResourceKindMCP, api.ResourceDisabled).Kind == api.SupportNative
	if got := slices.Contains(args, "--strict-mcp-config"); got != declared {
		t.Fatalf("claude-cli declares mcp/disabled native=%v, argv was %v", declared, args)
	}

	// gemini-cli declares it unsupported: the request is accepted and dropped.
	geminiArgs, err := buildGeminiCLIArgs("gemini-3.5-flash", req)
	if err != nil {
		t.Fatalf("buildGeminiCLIArgs: %v", err)
	}
	for _, arg := range geminiArgs {
		if strings.Contains(arg, "mcp") {
			t.Fatalf("gemini-cli declares mcp/disabled unsupported but argv mentions mcp: %v", geminiArgs)
		}
	}
}
