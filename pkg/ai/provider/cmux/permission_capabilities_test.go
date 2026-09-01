package cmux

import (
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/api"
)

// The cmux transports are the reason PermissionEffects.Flag is per-backend rather
// than per-provider: claude-cmux emits `--permission-mode default` for the unset
// posture where claude-cli omits the flag entirely. These tests pin that
// difference so the declaration keeps telling the truth about both.

func TestDeclaredClaudeCmuxPostureMatchesCommand(t *testing.T) {
	caps := api.PermissionCapabilitiesFor(api.Anthropic.Runtime(api.ModeCmux))
	for _, mode := range api.AllPermissionModes() {
		t.Run(string(mode), func(t *testing.T) {
			support := caps.ModeSupport(mode)
			if !support.Honoured() {
				t.Skipf("claude-cmux declares %s as %s", mode, support.Kind)
			}
			cmd := AgentCommand(AgentCommandOpts{Agent: "claude", PermissionMode: mode})
			if !strings.Contains(cmd, support.Effects.Flag) {
				t.Fatalf("declared %q for %s, command was %q", support.Effects.Flag, mode, cmd)
			}
		})
	}
}

// TestDeclaredCodexCmuxCarriesNoPermissionFlag pins the shape that makes codex's
// row a sandbox/approval pair rather than a flag: the command itself never
// mentions the posture, which rides on the typed cmux options instead.
func TestDeclaredCodexCmuxCarriesNoPermissionFlag(t *testing.T) {
	caps := api.PermissionCapabilitiesFor(api.OpenAI.Runtime(api.ModeCmux))
	for _, mode := range api.AllPermissionModes() {
		if flag := caps.ModeSupport(mode).Effects.Flag; flag != "" {
			t.Fatalf("codex-cmux declares flag %q for %s, but codex has no permission-mode flag", flag, mode)
		}
		if cmd := AgentCommand(AgentCommandOpts{Agent: "codex", PermissionMode: mode}); strings.Contains(cmd, "--permission-mode") {
			t.Fatalf("codex command carried a permission flag: %q", cmd)
		}
	}
}

// TestDeclaredClaudeCmuxToolPolicyMatchesCommand proves the agent-provenance row
// for the terminal transport: cmux forwards both claude tool flags, so allow and
// deny are declared native there just as they are on claude-cli.
func TestDeclaredClaudeCmuxToolPolicyMatchesCommand(t *testing.T) {
	caps := api.PermissionCapabilitiesFor(api.Anthropic.Runtime(api.ModeCmux))
	cmd := AgentCommand(AgentCommandOpts{
		Agent: "claude", AllowedTools: []string{"Read"}, DisallowedTools: []string{"Bash"},
	})
	for _, pair := range []struct {
		policy api.ToolPolicy
		flag   string
	}{
		{api.ToolPolicyAllow, "--allowedTools"},
		{api.ToolPolicyDeny, "--disallowedTools"},
	} {
		declared := caps.ToolPolicySupport(api.ProvenanceAgent, pair.policy).Kind == api.SupportNative
		if got := strings.Contains(cmd, pair.flag); got != declared {
			t.Fatalf("declared %s native=%v but command carries %s = %v: %q", pair.policy, declared, pair.flag, got, cmd)
		}
	}
}
