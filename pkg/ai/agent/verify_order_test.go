package agent

import (
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A round must stop at its first failing verifier (issue #40 R5.1). Before
// this rule, a later passing judge became the round's final verdict and
// masked the failure — [fail, pass] reported the run as verified.
func TestVerify_StopsAtFirstFailure(t *testing.T) {
	judgeCalls := 0
	r := &Runner[string]{Hooks: []any{
		verifyHook{name: "cmd", fn: func(*HookContext) (VerifyResult, error) {
			return VerifyResult{Valid: false, Retry: &ai.Request{}}, nil
		}},
		verifyHook{name: "judge", fn: func(*HookContext) (VerifyResult, error) {
			judgeCalls++
			return VerifyResult{Valid: true}, nil
		}},
	}}

	verdicts, retry, allValid, err := r.verify(&HookContext{})

	require.NoError(t, err)
	assert.False(t, allValid, "a failing verifier must fail the round regardless of hooks behind it")
	assert.Equal(t, 0, judgeCalls, "hooks after the failure must not run (R5.1)")
	require.Len(t, verdicts, 1)
	assert.False(t, verdicts[len(verdicts)-1].Valid, "the round's last verdict must be the failure")
	assert.NotNil(t, retry, "the failing hook's retry must be proposed")
	assert.False(t, verifyPassed(verdicts))
}

func TestVerify_AllPassingRunsEveryHook(t *testing.T) {
	calls := 0
	hook := verifyHook{name: "ok", fn: func(*HookContext) (VerifyResult, error) {
		calls++
		return VerifyResult{Valid: true}, nil
	}}
	r := &Runner[string]{Hooks: []any{hook, hook, hook}}

	verdicts, retry, allValid, err := r.verify(&HookContext{})

	require.NoError(t, err)
	assert.True(t, allValid)
	assert.Nil(t, retry)
	assert.Equal(t, 3, calls)
	assert.Len(t, verdicts, 3)
	assert.True(t, verifyPassed(verdicts))
}
