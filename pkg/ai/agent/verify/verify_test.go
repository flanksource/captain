package verify

import (
	"context"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCmdVerifier_PassAndFail(t *testing.T) {
	pass := &CmdVerifier{Cmd: "true"}
	v, err := pass.Verify(context.Background(), t.TempDir(), nil)
	require.NoError(t, err)
	assert.True(t, v.OK)

	fail := &CmdVerifier{Cmd: "sh", Args: []string{"-c", "echo boom; exit 1"}}
	v, err = fail.Verify(context.Background(), t.TempDir(), nil)
	require.NoError(t, err)
	assert.False(t, v.OK)
	assert.Contains(t, v.Feedback, "boom")
}

func TestPlugin_PassesChangedOnlyWhenScopeChanged(t *testing.T) {
	var seen []string
	rec := FuncVerifier(func(_ context.Context, _ string, changed []string) (Verdict, error) {
		seen = changed
		return Verdict{OK: true}, nil
	})
	p := New("rec", rec)

	hc := &agent.HookContext{
		Context:  context.Background(),
		Request:  &ai.Request{},
		Response: &ai.Response{Workspace: &api.Workspace{Changed: []string{"a.go"}}},
		Scope:    agent.ScopeChanged,
	}
	res, err := p.Verify(hc)
	require.NoError(t, err)
	assert.True(t, res.Valid)
	assert.Equal(t, []string{"a.go"}, seen)

	hc.Scope = agent.ScopeAll
	_, err = p.Verify(hc)
	require.NoError(t, err)
	assert.Nil(t, seen, "ScopeAll ⇒ verifier gets nil (whole-tree)")
}

func TestPlugin_FailBuildsRetryWithFeedback(t *testing.T) {
	rec := FuncVerifier(func(_ context.Context, _ string, _ []string) (Verdict, error) {
		return Verdict{OK: false, Reason: "lint", Feedback: "fix line 7"}, nil
	})
	p := New("rec", rec)
	hc := &agent.HookContext{
		Context:  context.Background(),
		Request:  &ai.Request{Prompt: api.Prompt{User: "do it"}},
		Response: &ai.Response{Workspace: &api.Workspace{}},
		Scope:    agent.ScopeAll,
	}
	res, err := p.Verify(hc)
	require.NoError(t, err)
	assert.False(t, res.Valid)
	require.NotNil(t, res.Retry)
	assert.Contains(t, res.Retry.Prompt.User, "do it")
	assert.Contains(t, res.Retry.Prompt.User, "fix line 7")
}
