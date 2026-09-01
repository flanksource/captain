package claudeagent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInitializeParams_PermissionMode pins the posture the SDK child is started
// with. An absent permissions block must resolve to the ask/deny default, never
// to bypass: "the caller declared no policy" is not "the caller granted
// everything", and CanUseTool is nil on every path except the chat server, so
// the unbrokered branch is the common one rather than the exotic one.
func TestInitializeParams_PermissionMode(t *testing.T) {
	broker := func(context.Context, ai.PermissionRequest) (ai.PermissionDecision, error) {
		return ai.PermissionDecision{Allow: true}, nil
	}

	tests := []struct {
		name         string
		mode         api.PermissionMode
		presets      []api.Preset
		broker       ai.PermissionFunc
		wantMode     string
		wantApproval string
	}{
		{
			name:         "unset and unbrokered falls back to default, not bypass",
			wantMode:     "default",
			wantApproval: "auto",
		},
		{
			name:         "unset and brokered defers to the broker",
			broker:       broker,
			wantMode:     "default",
			wantApproval: "ask",
		},
		{
			name:         "an explicit bypass is still honoured",
			mode:         api.PermissionBypass,
			wantMode:     "bypassPermissions",
			wantApproval: "auto",
		},
		{
			name:         "plan passes through",
			mode:         api.PermissionPlan,
			wantMode:     "plan",
			wantApproval: "auto",
		},
		{
			name:         "the edit preset does not change sandbox approval",
			presets:      []api.Preset{api.PresetEdit},
			wantMode:     "default",
			wantApproval: "auto",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Provider{cfg: ai.Config{CanUseTool: tt.broker}}
			params, err := p.initializeParams(ai.Request{
				Permissions: api.Permissions{Mode: tt.mode, Presets: tt.presets},
			})
			require.NoError(t, err)
			assert.Equal(t, tt.wantMode, params.PermissionMode)
			assert.Equal(t, tt.wantApproval, params.ApprovalMode)
		})
	}
}

// TestInitializeParams_EditPresetAllowlist keeps --edit's curated allowlist
// attached to the preset rather than to the absence of a permission mode.
func TestInitializeParams_EditPresetAllowlist(t *testing.T) {
	p := &Provider{}
	params, err := p.initializeParams(ai.Request{
		Permissions: api.Permissions{Presets: []api.Preset{api.PresetEdit}},
	})
	require.NoError(t, err)
	assert.Equal(t, safeEditAllowlist, params.AllowedTools)

	explicit, err := p.initializeParams(ai.Request{
		Permissions: api.Permissions{
			Presets: []api.Preset{api.PresetEdit},
			Tools:   api.Tools{"Read": api.ToolPolicyAllow},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"Read"}, explicit.AllowedTools,
		"an explicit allowlist must not be replaced by the preset's")
}

// TestInitializeParams_NormalizesToolModes pins that the SDK child is configured
// from AllowList/DenyList rather than by scanning the map for one spelling of a
// deny: the legacy `modes: {Bash: off}` and an explicit deny are the same policy
// once decoded, and both must reach disallowedTools.
func TestInitializeParams_NormalizesToolModes(t *testing.T) {
	p := &Provider{}
	var tools api.Tools
	require.NoError(t, json.Unmarshal(
		[]byte(`{"deny":["WebFetch"],"modes":{"Bash":"off","Read":"on"}}`), &tools))
	params, err := p.initializeParams(ai.Request{
		Permissions: api.Permissions{Tools: tools},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"Bash", "WebFetch"}, params.DisallowedTools,
		"an off tool mode is a deny and must reach disallowedTools")
	// `on` resolves to auto — the agent's normal behaviour — so it must not
	// narrow the run into a one-tool allowlist.
	assert.Empty(t, params.AllowedTools)
}

// TestExecuteStream_RefusesUnenforceableAskPolicy keeps the advertised
// tool-policy support honest: claude-agent carries allow/deny lists only, so an
// `ask` policy must fail loudly rather than resolve to "allowed".
func TestExecuteStream_RefusesUnenforceableAskPolicy(t *testing.T) {
	p := &Provider{}
	_, err := p.ExecuteStream(context.Background(), ai.Request{
		Permissions: api.Permissions{
			Tools: api.Tools{"Bash": api.ToolPolicyAsk},
		},
	})
	assert.ErrorContains(t, err, "ask")
	assert.ErrorContains(t, err, "Bash")
}
