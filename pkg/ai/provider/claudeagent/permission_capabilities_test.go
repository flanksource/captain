package claudeagent

import (
	"context"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// claude-agent is the transport that makes PermissionEffects.Flag per-backend:
// the posture rides on a `permissionMode` initialize param rather than argv, and
// unlike claude-cli it sends `default` explicitly. These tests hold the declared
// table to what initializeParams actually produces.

func TestDeclaredPostureMatchesInitializeParams(t *testing.T) {
	caps := api.PermissionCapabilitiesFor(api.Anthropic.Runtime(api.ModeAgent))
	p := &Provider{}
	for _, mode := range api.AllPermissionModes() {
		t.Run(string(mode), func(t *testing.T) {
			support := caps.ModeSupport(mode)
			require.True(t, support.Honoured(), "claude-agent should honour every posture, %s is %s", mode, support.Kind)

			want, ok := strings.CutPrefix(support.Effects.Flag, "permissionMode=")
			require.True(t, ok, "claude-agent declares its posture as an initialize param, got %q", support.Effects.Flag)

			params, err := p.initializeParams(ai.Request{Permissions: api.Permissions{Mode: mode}})
			require.NoError(t, err)
			assert.Equal(t, want, params.PermissionMode)
		})
	}
}

// TestDeclaredDefaultPostureIsSentExplicitly is the difference from claude-cli,
// which omits the flag and lets the CLI pick. Declaring an empty Flag here would
// be wrong in a way no posture test would otherwise notice.
func TestDeclaredDefaultPostureIsSentExplicitly(t *testing.T) {
	declared := api.PermissionCapabilitiesFor(api.Anthropic.Runtime(api.ModeAgent)).
		ModeSupport(api.PermissionDefault).Effects.Flag
	require.NotEmpty(t, declared, "claude-agent sends the unset posture explicitly")

	params, err := (&Provider{}).initializeParams(ai.Request{})
	require.NoError(t, err)
	assert.Equal(t, "permissionMode="+params.PermissionMode, declared)
}

// TestDeclaredCallerToolBrokerMatchesApprovalMode pins the requires-broker cell.
// `ask` on a caller tool is only enforceable when CanUseTool is attached — the
// SDK is told to consult the broker instead of auto-approving — which is exactly
// what SupportRequiresBroker means and why it is not simply "supported".
func TestDeclaredCallerToolBrokerMatchesApprovalMode(t *testing.T) {
	support := api.PermissionCapabilitiesFor(api.Anthropic.Runtime(api.ModeAgent)).
		ToolPolicySupport(api.ProvenanceCaller, api.ToolPolicyAsk)
	require.Equal(t, api.SupportRequiresBroker, support.Kind)

	unbrokered, err := (&Provider{}).initializeParams(ai.Request{})
	require.NoError(t, err)
	assert.Equal(t, "auto", unbrokered.ApprovalMode, "without a broker there is nothing to ask")

	brokered, err := (&Provider{cfg: ai.Config{CanUseTool: func(context.Context, ai.PermissionRequest) (ai.PermissionDecision, error) {
		return ai.PermissionDecision{Allow: true}, nil
	}}}).initializeParams(ai.Request{})
	require.NoError(t, err)
	assert.Equal(t, "ask", brokered.ApprovalMode, "a broker is what makes ask enforceable")
}

// TestDeclaredMCPDisableIsRefusedNotDropped pins why claude-agent declares
// ResourceKindMCP/disabled unsupported rather than native: with caller tools in
// play the request is refused, and with none it is silently dropped. Neither is
// enforcement, and the refusal is the only part a caller can see.
func TestDeclaredMCPDisableIsRefusedNotDropped(t *testing.T) {
	require.Equal(t, api.SupportUnsupported,
		api.PermissionCapabilitiesFor(api.Anthropic.Runtime(api.ModeAgent)).
			ResourceSupport(api.ResourceKindMCP, api.ResourceDisabled).Kind)

	// No caller tools: the request reaches nothing at all.
	p := &Provider{}
	require.NoError(t, p.prepareCallerTools(ai.Request{Permissions: api.Permissions{MCP: api.MCP{Disabled: true}}}))
	assert.Nil(t, callerToolServers(p.callerTools),
		"mcp.disabled is accepted here and changes nothing")
}
