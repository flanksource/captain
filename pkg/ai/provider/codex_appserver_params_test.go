package provider

import (
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/shell"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComposePrompt(t *testing.T) {
	assert.Equal(t, "task", composePrompt(req(api.Prompt{User: "task"})))
	assert.Equal(t, "be brief\n\ntask", composePrompt(req(api.Prompt{User: "task", System: "be brief"})))
	assert.Equal(t, "task\n\ntail", composePrompt(req(api.Prompt{User: "task", AppendSystem: "tail"})))
	assert.Equal(t, "sys\n\ntask\n\ntail",
		composePrompt(req(api.Prompt{User: "task", System: "sys", AppendSystem: "tail"})))
}

func TestBuildThreadStartParams_Safety(t *testing.T) {
	tests := []struct {
		name         string
		req          ai.Request
		wantSandbox  string
		wantApproval string
		wantEphem    bool
		wantNoMCP    bool
	}{
		{name: "default is read-only on-request", req: req(api.Prompt{User: "p"}), wantSandbox: "read-only", wantApproval: "on-request"},
		{
			name:        "edit maps to workspace-write",
			req:         ai.Request{Prompt: api.Prompt{User: "p"}, Permissions: api.Permissions{Presets: []api.Preset{api.PresetEdit}}},
			wantSandbox: "workspace-write", wantApproval: "on-request",
		},
		{
			name:        "explicit permission mode skips workspace-write default",
			req:         ai.Request{Prompt: api.Prompt{User: "p"}, Permissions: api.Permissions{Presets: []api.Preset{api.PresetEdit}, Mode: api.PermissionDefault}},
			wantSandbox: "read-only", wantApproval: "on-request",
		},
		{
			name:        "bypass permissions maps to danger-full-access never",
			req:         ai.Request{Prompt: api.Prompt{User: "p"}, Permissions: api.Permissions{Mode: api.PermissionBypass}},
			wantSandbox: "danger-full-access", wantApproval: "never",
		},
		{
			name:        "no-memory sets ephemeral",
			req:         ai.Request{Prompt: api.Prompt{User: "p"}, Memory: api.Memory{SkipMemory: true}},
			wantSandbox: "read-only", wantApproval: "on-request", wantEphem: true,
		},
		{
			name:        "bare sets ephemeral",
			req:         ai.Request{Prompt: api.Prompt{User: "p"}, Memory: api.Memory{Bare: true}},
			wantSandbox: "read-only", wantApproval: "on-request", wantEphem: true,
		},
		{
			name:        "no-mcp sets empty mcp_servers override",
			req:         ai.Request{Prompt: api.Prompt{User: "p"}, Permissions: api.Permissions{MCP: api.MCP{Disabled: true}}},
			wantSandbox: "read-only", wantApproval: "on-request", wantNoMCP: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := buildThreadStartParams("gpt-5", tc.req, nil)
			assert.Equal(t, tc.wantSandbox, p["sandbox"])
			assert.Equal(t, tc.wantApproval, p["approvalPolicy"])
			_, hasEphem := p["ephemeral"]
			assert.Equal(t, tc.wantEphem, hasEphem)
			cfg, hasCfg := p["config"].(map[string]any)
			assert.Equal(t, tc.wantNoMCP, hasCfg)
			if tc.wantNoMCP {
				assert.Equal(t, map[string]any{}, cfg["mcp_servers"])
			}
		})
	}
}

func TestBuildThreadStartParams_CwdAndModel(t *testing.T) {
	p := buildThreadStartParams("gpt-5", ai.Request{
		Prompt: api.Prompt{User: "p"}, Setup: &shell.Setup{Cwd: "/repo"},
	}, nil)
	assert.Equal(t, "/repo", p["cwd"])
	assert.Equal(t, "gpt-5", p["model"])
	noModel := buildThreadStartParams("", req(api.Prompt{User: "p"}), nil)
	_, hasModel := noModel["model"]
	assert.False(t, hasModel, "empty model must be omitted")
}

func TestBuildResumeParams(t *testing.T) {
	p := buildResumeParams(ai.Request{SessionID: "thread-9", Setup: &shell.Setup{Cwd: "/repo"}}, nil)
	assert.Equal(t, "thread-9", p["threadId"])
	assert.Equal(t, "/repo", p["cwd"])
}

func TestHandleApproval_AutoApproves(t *testing.T) {
	c, err := NewCodexAppServer(ai.Config{Model: api.Model{Name: "m"}})
	require.NoError(t, err)
	tests := []struct {
		method string
		key    string
		want   any
	}{
		{"execCommandApproval", "decision", "approved"},
		{"applyPatchApproval", "decision", "approved"},
		{"item/commandExecution/requestApproval", "decision", "accept"},
		{"item/fileChange/requestApproval", "decision", "accept"},
		{"some/unknown/approval", "decision", "approved"},
	}
	for _, tc := range tests {
		t.Run(tc.method, func(t *testing.T) {
			res, rpcErr := c.handleApproval(tc.method, nil)
			assert.Nil(t, rpcErr)
			m, ok := res.(map[string]string)
			require.True(t, ok, "decision approvals return a string map")
			assert.Equal(t, tc.want, m[tc.key])
		})
	}
	res, rpcErr := c.handleApproval("item/permissions/requestApproval", nil)
	assert.Nil(t, rpcErr)
	perm, ok := res.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "turn", perm["scope"])
	assert.NotNil(t, perm["permissions"])
}
