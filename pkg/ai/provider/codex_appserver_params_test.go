package provider

import (
	"runtime"
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
		{name: "native defaults to read-only on-request", req: codexNativeRequest(codexNativeRequestOptions{}), wantSandbox: "read-only", wantApproval: "on-request"},
		{
			name: "edit maps to workspace-write",
			req: ai.Request{Prompt: api.Prompt{User: "p"}, Permissions: api.Permissions{Mode: api.PermissionAcceptEdits}, Sandbox: &api.SandboxRef{
				Mode: api.SandboxNative,
				Policy: &api.NativeSandboxPolicy{Filesystem: &api.SandboxFilesystemPolicy{
					Access: api.SandboxFilesystemWorkspaceWrite,
				}},
			}},
			wantSandbox: "workspace-write", wantApproval: "on-request",
		},
		{
			name: "plan keeps the native sandbox read-only",
			req: ai.Request{Prompt: api.Prompt{User: "p"}, Permissions: api.Permissions{Mode: api.PermissionPlan}, Sandbox: &api.SandboxRef{
				Mode: api.SandboxNative,
				Policy: &api.NativeSandboxPolicy{Filesystem: &api.SandboxFilesystemPolicy{
					Access: api.SandboxFilesystemWorkspaceWrite,
				}},
			}},
			wantSandbox: "read-only", wantApproval: "on-request",
		},
		{
			name: "bypass permissions maps to danger-full-access never",
			req: ai.Request{
				Prompt: api.Prompt{User: "p"}, Permissions: api.Permissions{Mode: api.PermissionBypass},
				Sandbox: &api.SandboxRef{Mode: api.SandboxOff},
			},
			wantSandbox: "danger-full-access", wantApproval: "never",
		},
		{
			name:        "no-memory sets ephemeral",
			req:         codexNativeRequest(codexNativeRequestOptions{Memory: api.Memory{SkipMemory: true}}),
			wantSandbox: "read-only", wantApproval: "on-request", wantEphem: true,
		},
		{
			name:        "bare sets ephemeral",
			req:         codexNativeRequest(codexNativeRequestOptions{Memory: api.Memory{Bare: true}}),
			wantSandbox: "read-only", wantApproval: "on-request", wantEphem: true,
		},
		{
			name:        "no-mcp sets empty mcp_servers override",
			req:         codexNativeRequest(codexNativeRequestOptions{Permissions: api.Permissions{MCP: api.MCP{Disabled: true}}}),
			wantSandbox: "read-only", wantApproval: "on-request", wantNoMCP: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := buildThreadStartParams("gpt-5", tc.req, nil)
			require.NoError(t, err)
			assert.Equal(t, true, p["experimentalRawEvents"])
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

type codexNativeRequestOptions struct {
	Memory      api.Memory
	Permissions api.Permissions
}

func codexNativeRequest(options codexNativeRequestOptions) ai.Request {
	permissions := options.Permissions
	if permissions.Mode == "" {
		permissions.Mode = api.PermissionDefault
	}
	return ai.Request{
		Prompt:      api.Prompt{User: "p"},
		Memory:      options.Memory,
		Permissions: permissions,
		Sandbox:     &api.SandboxRef{Mode: api.SandboxNative},
	}
}

func TestBuildThreadStartParams_CwdAndModel(t *testing.T) {
	p, err := buildThreadStartParams("gpt-5", ai.Request{
		Prompt: api.Prompt{User: "p"}, Setup: &shell.Setup{Cwd: "/repo"},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "/repo", p["cwd"])
	assert.Equal(t, "gpt-5", p["model"])
	noModel, err := buildThreadStartParams("", ai.Request{}, nil)
	require.NoError(t, err)
	_, hasModel := noModel["model"]
	assert.False(t, hasModel, "empty model must be omitted")
}

func TestBuildResumeParams(t *testing.T) {
	p := buildResumeParams(ai.Request{SessionID: "thread-9", Setup: &shell.Setup{Cwd: "/repo"}}, nil)
	assert.Equal(t, "thread-9", p["threadId"])
	assert.Equal(t, "/repo", p["cwd"])
}

// TestHandleApproval_AnswersFromPosture pins the approval policy. An approval
// request is codex asking to exceed the sandbox it was started with, so only a
// run that declared full access may accept one — otherwise buildThreadStartParams'
// sandbox and approvalPolicy are advisory and `mode: plan` gates nothing.
// The decision vocabularies are codex's own, from `codex app-server
// generate-json-schema`: accept|decline (item/*) and approved|denied (legacy).
func TestHandleApproval_AnswersFromPosture(t *testing.T) {
	methods := []struct{ method, accept, decline string }{
		{"execCommandApproval", "approved", "denied"},
		{"applyPatchApproval", "approved", "denied"},
		{"item/commandExecution/requestApproval", "accept", "decline"},
		{"item/fileChange/requestApproval", "accept", "decline"},
		{"some/unknown/approval", "approved", "denied"},
	}
	postures := []struct {
		name       string
		sandbox    *api.SandboxRef
		mode       api.PermissionMode
		wantAccept bool
	}{
		{
			name:    "the default read-only posture declines",
			sandbox: &api.SandboxRef{Mode: api.SandboxNative},
		},
		{
			name:    "plan declines",
			sandbox: &api.SandboxRef{Mode: api.SandboxNative},
			mode:    api.PermissionPlan,
		},
		{
			name: "the edit preset declines escalation beyond its workspace",
			sandbox: &api.SandboxRef{Mode: api.SandboxNative, Policy: &api.NativeSandboxPolicy{
				Filesystem: &api.SandboxFilesystemPolicy{Access: api.SandboxFilesystemWorkspaceWrite},
			}},
		},
		{
			name:       "an explicit bypass has already granted it",
			sandbox:    &api.SandboxRef{Mode: api.SandboxOff},
			wantAccept: true,
		},
	}

	for _, posture := range postures {
		t.Run(posture.name, func(t *testing.T) {
			c, err := NewCodexAppServer(ai.Config{Model: api.Model{Name: "m"}})
			require.NoError(t, err)
			c.setPosture(postureFor(ai.Request{
				Sandbox: posture.sandbox, Permissions: api.Permissions{Mode: posture.mode},
			}))

			for _, m := range methods {
				want := m.decline
				if posture.wantAccept {
					want = m.accept
				}
				res, rpcErr := c.handleApproval(m.method, nil)
				assert.Nil(t, rpcErr)
				decision, ok := res.(map[string]string)
				require.True(t, ok, "%s returns a string map", m.method)
				assert.Equal(t, want, decision["decision"], m.method)
			}

			// Additional permissions are never granted: the thread already carries
			// everything the run declared.
			res, rpcErr := c.handleApproval("item/permissions/requestApproval", nil)
			assert.Nil(t, rpcErr)
			perm, ok := res.(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "turn", perm["scope"])
			assert.Empty(t, perm["permissions"])
		})
	}
}

// TestBeginTurn_ConcurrentTurnCannotEscalateTheInFlightPosture pins the lock
// ordering: a bypass turn queued behind an in-flight restricted turn must not
// publish its posture until the restricted turn releases turnMu, or the
// restricted turn's approvals would be answered with the bypass policy.
func TestBeginTurn_ConcurrentTurnCannotEscalateTheInFlightPosture(t *testing.T) {
	c, err := NewCodexAppServer(ai.Config{Model: api.Model{Name: "m"}})
	require.NoError(t, err)

	c.beginTurn(ai.Request{Sandbox: &api.SandboxRef{Mode: api.SandboxNative}})

	queued := make(chan struct{})
	go func() {
		defer close(queued)
		c.beginTurn(ai.Request{Sandbox: &api.SandboxRef{Mode: api.SandboxOff}})
		c.turnMu.Unlock()
	}()

	// The queued turn is blocked on turnMu, so approvals raised by the still
	// in-flight restricted turn keep declining.
	for i := 0; i < 50; i++ {
		res, rpcErr := c.handleApproval("item/commandExecution/requestApproval", nil)
		assert.Nil(t, rpcErr)
		require.Equal(t, "decline", res.(map[string]string)["decision"],
			"the queued bypass turn overwrote the in-flight restricted posture")
		runtime.Gosched()
	}

	c.turnMu.Unlock()
	<-queued
	res, rpcErr := c.handleApproval("item/commandExecution/requestApproval", nil)
	assert.Nil(t, rpcErr)
	assert.Equal(t, "accept", res.(map[string]string)["decision"],
		"the bypass turn's posture takes effect once it owns the turn")
}

// TestHandleApproval_PostureDefaultsClosed guards the zero value: a provider
// that has not started a turn must not answer an approval as though it had.
func TestHandleApproval_PostureDefaultsClosed(t *testing.T) {
	c, err := NewCodexAppServer(ai.Config{Model: api.Model{Name: "m"}})
	require.NoError(t, err)
	res, rpcErr := c.handleApproval("item/commandExecution/requestApproval", nil)
	assert.Nil(t, rpcErr)
	assert.Equal(t, "decline", res.(map[string]string)["decision"])
}
