package verify

import (
	"context"
	"testing"

	"github.com/flanksource/captain/pkg/ai/agent"
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
	rec := FuncVerifier(func(_ context.Context, _ string, changed []string) (agent.Verdict, error) {
		seen = changed
		return agent.Verdict{OK: true}, nil
	})
	p := New("rec", rec)

	_, err := p.Verify(&agent.RunContext{Ctx: context.Background(), Scope: agent.ScopeChanged, ChangedFiles: []string{"a.go"}}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"a.go"}, seen)

	_, err = p.Verify(&agent.RunContext{Ctx: context.Background(), Scope: agent.ScopeAll, ChangedFiles: []string{"a.go"}}, nil)
	require.NoError(t, err)
	assert.Nil(t, seen, "ScopeAll ⇒ verifier gets nil (whole-tree)")
}
