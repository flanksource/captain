package verify

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCmdVerifier_TimeoutKillsProcessGroup(t *testing.T) {
	// `sleep 30 & wait` puts a child in the group holding our output pipe. If
	// only the shell's pid were signalled, the orphaned sleep would keep the
	// pipe open and this test would stall on WaitDelay; the group kill brings
	// the verdict back immediately.
	v := &CmdVerifier{Cmd: "sh", Args: []string{"-c", "sleep 30 & wait"}, Timeout: 200 * time.Millisecond}

	start := time.Now()
	verdict, err := v.Verify(context.Background(), t.TempDir(), nil)

	require.NoError(t, err)
	assert.False(t, verdict.OK)
	assert.Contains(t, verdict.Reason, "timed out after")
	assert.Less(t, time.Since(start), 3*time.Second, "group kill must not wait for the orphaned child")
}

func TestCmdVerifier_ParentCancellationIsAnErrorNotAVerdict(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	v := &CmdVerifier{Cmd: "sleep", Args: []string{"5"}}
	_, err := v.Verify(ctx, t.TempDir(), nil)

	require.ErrorIs(t, err, context.Canceled)
}

func TestCmdVerifier_OutputIsTailBoundedWithMarker(t *testing.T) {
	v := &CmdVerifier{
		Cmd:          "sh",
		Args:         []string{"-c", "i=0; while [ $i -lt 2000 ]; do echo line-$i; i=$((i+1)); done; exit 1"},
		FeedbackTail: 512,
	}

	verdict, err := v.Verify(context.Background(), t.TempDir(), nil)

	require.NoError(t, err)
	assert.False(t, verdict.OK)
	assert.LessOrEqual(t, len(verdict.Feedback), 512+len("[output truncated]\n"))
	assert.True(t, strings.HasPrefix(verdict.Feedback, "[output truncated]"), "truncation must be marked")
	assert.Contains(t, verdict.Feedback, "line-1999", "the tail, not the head, must be kept")
}

func TestCmdVerifier_StartFailureFeedsBackTheError(t *testing.T) {
	v := &CmdVerifier{Cmd: "captain-no-such-binary"}

	verdict, err := v.Verify(context.Background(), t.TempDir(), nil)

	require.NoError(t, err)
	assert.False(t, verdict.OK)
	assert.Contains(t, verdict.Feedback, "captain-no-such-binary")
}

// A wrapper that returns no environment must leave the pre-wrap boundary in
// place, not fall through to full process inheritance: the git-agent hook path
// hands a deliberately reduced Env to Wrap, and inheriting the process
// environment would silently expose every ambient credential to an
// agent-authored command (issue #40).
func TestCmdVerifier_WrapWithNilEnvKeepsTheDeclaredBoundary(t *testing.T) {
	t.Setenv("CAPTAIN_TEST_AMBIENT_SECRET", "leaked")

	v := &CmdVerifier{
		Cmd:  "sh",
		Args: []string{"-c", `test -z "$CAPTAIN_TEST_AMBIENT_SECRET" && test "$MARKER" = ok`},
		Env:  []string{"PATH=" + os.Getenv("PATH"), "MARKER=ok"},
		Wrap: func(_ context.Context, cmd string, args, _ []string) (string, []string, []string, error) {
			return cmd, args, nil, nil // a wrapper that supplies no environment
		},
	}

	verdict, err := v.Verify(context.Background(), t.TempDir(), nil)

	require.NoError(t, err)
	assert.True(t, verdict.OK, "declared env must reach the command and the ambient secret must not: %s", verdict.Feedback)
}

// A parent deadline shorter than the verifier's own Timeout is the RUN's
// cancellation, not the command's: it must come back as an error, never as a
// verdict blaming the command for "timing out after <Timeout>".
func TestCmdVerifier_ParentDeadlineIsNotTheCommandsTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	v := &CmdVerifier{Cmd: "sleep", Args: []string{"30"}, Timeout: time.Hour}
	_, err := v.Verify(ctx, t.TempDir(), nil)

	require.ErrorIs(t, err, context.DeadlineExceeded)
}
