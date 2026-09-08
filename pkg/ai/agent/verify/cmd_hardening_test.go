package verify

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
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

// writerFunc adapts a function to io.Writer so a test can observe or fail a
// write without defining a type per case.
type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// The whole point of Output is that bytes are visible while the command is
// still running. A tail buffer read after the process exits cannot do this, so
// this is the test that fails against the old design.
func TestCmdVerifier_OutputReachesTheSinkBeforeTheProcessExits(t *testing.T) {
	seen := make(chan string, 16)
	sink := writerFunc(func(p []byte) (int, error) {
		select {
		case seen <- string(p):
		default:
		}
		return len(p), nil
	})

	v := &CmdVerifier{Cmd: "sh", Args: []string{"-c", "echo streaming; sleep 5"}, Output: sink}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = v.Verify(ctx, t.TempDir(), nil)
	}()

	select {
	case got := <-seen:
		assert.Contains(t, got, "streaming")
	case <-time.After(2 * time.Second):
		t.Fatal("no output reached the sink while the command was still running")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Verify did not return after cancellation")
	}
}

// io.MultiWriter stops at the first writer that errors. A live sink is a
// courtesy; it must never be able to cut short the output the next iteration is
// judged on.
func TestCmdVerifier_OutputSinkFailureDoesNotTruncateFeedback(t *testing.T) {
	sink := writerFunc(func([]byte) (int, error) { return 0, errors.New("sink is gone") })

	v := &CmdVerifier{
		Cmd:    "sh",
		Args:   []string{"-c", "i=0; while [ $i -lt 300 ]; do echo line-$i; i=$((i+1)); done; exit 1"},
		Output: sink,
	}

	verdict, err := v.Verify(context.Background(), t.TempDir(), nil)

	require.NoError(t, err)
	assert.False(t, verdict.OK)
	assert.Contains(t, verdict.Feedback, "line-299", "a failing sink must not truncate the feedback tail")
}

// os/exec gives the child one pipe only while Stdout and Stderr are
// interface-equal. Two writer values would split them onto two pipes and race
// the sink, losing the child's own ordering.
func TestCmdVerifier_OutputCarriesBothStreamsInOrder(t *testing.T) {
	var sink safeStringBuilder

	v := &CmdVerifier{Cmd: "sh", Args: []string{"-c", "echo out; echo err 1>&2; exit 1"}, Output: &sink}

	verdict, err := v.Verify(context.Background(), t.TempDir(), nil)

	require.NoError(t, err)
	assert.Equal(t, "out\nerr\n", sink.String(), "both streams, in the order the child wrote them")
	assert.Contains(t, verdict.Feedback, "out")
	assert.Contains(t, verdict.Feedback, "err")
}

// Behavioural, so it does not pin the constant: a failing run of a few hundred
// KB must reach the next iteration whole.
func TestCmdVerifier_DefaultTailKeepsALargeFailingRun(t *testing.T) {
	v := &CmdVerifier{
		Cmd:  "sh",
		Args: []string{"-c", `awk 'BEGIN{for(i=0;i<20000;i++) print "line-"i}'; exit 1`},
	}

	verdict, err := v.Verify(context.Background(), t.TempDir(), nil)

	require.NoError(t, err)
	assert.False(t, verdict.OK)
	assert.False(t, strings.HasPrefix(verdict.Feedback, "[output truncated]"),
		"a few hundred KB of failure output must survive the default tail")
	assert.Contains(t, verdict.Feedback, "line-0", "the head must survive too when nothing was dropped")
	assert.Contains(t, verdict.Feedback, "line-19999")
}

// safeStringBuilder is a mutex-guarded strings.Builder: the exec copy goroutine
// writes from another goroutine, so an unguarded builder races the assertion.
type safeStringBuilder struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *safeStringBuilder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeStringBuilder) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
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
