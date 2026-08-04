// The nested relay (§6.2 step 9): still inside the sidecar's pre-receive, the
// vetted work is pushed onward to the supervisor's mailbox and the upstream
// sideband streams back out through the sidecar's own stderr. The relay lives
// in pre-receive because post-receive can no longer reject (R6.6/H16); it
// unsets GIT_QUARANTINE_PATH and keeps the inherited object directories, so
// no object is ever copied out of quarantine (R1.4).
package gitagent

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// RelayTarget is where and how the sidecar reaches the supervisor mailbox.
type RelayTarget struct {
	URL             string `json:"url"`
	HostFingerprint string `json:"hostFingerprint"`
	KeyPath         string `json:"keyPath"`
	SSHCommand      string `json:"sshCommand,omitempty"` // "" ⇒ this binary's transport
}

// BuildResultCommit squashes the agent's branch tip into the single result
// commit the protocol requires: tree = the tip's tree, parent = the dispatch
// commit (§3.2). Written through the hook environment, so in pre-receive the
// new object lands in quarantine and is discarded with a rejected push.
func BuildResultCommit(ctx context.Context, repo string, env []string, tip, dispatchCommit string) (string, error) {
	tree, err := runGit(ctx, repo, env, "rev-parse", tip+"^{tree}")
	if err != nil {
		return "", err
	}
	cenv := envWith(env,
		"GIT_AUTHOR_NAME=captain",
		"GIT_AUTHOR_EMAIL=captain@localhost",
		"GIT_COMMITTER_NAME=captain",
		"GIT_COMMITTER_EMAIL=captain@localhost",
	)
	return runGitIn(ctx, repo, cenv,
		strings.NewReader("captain result\n"),
		"commit-tree", tree, "-p", dispatchCommit)
}

// Relay pushes result+control atomically to the mailbox, streaming the
// upstream's stderr through sideband. A non-zero upstream exit is the
// caller's signal to reject the agent's push (R6.7).
func Relay(ctx context.Context, repo string, hookEnv []string, target RelayTarget, envelope Envelope, result, control string, sideband io.Writer) error {
	resultRef, err := ResultRef(envelope.Task, envelope.Attempt)
	if err != nil {
		return err
	}
	controlRef, err := ControlRef(envelope.Task, envelope.Attempt)
	if err != nil {
		return err
	}
	opts, err := envelope.Encode()
	if err != nil {
		return err
	}
	args := []string{"push", "--atomic"}
	for _, o := range opts {
		args = append(args, "--push-option="+o)
	}
	args = append(args, target.URL, result+":"+resultRef, control+":"+controlRef)

	pairs, err := transportPairs(target.SSHCommand, target.KeyPath, target.HostFingerprint)
	if err != nil {
		return err
	}
	// R1.4: unset only GIT_QUARANTINE_PATH; the object-directory variables
	// stay so the quarantined objects remain readable for the outbound pack.
	env := envWith(RelayEnv(hookEnv), pairs...)
	code, out, err := gitExitCodeStderr(ctx, repo, env, sideband, args...)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("supervisor rejected attempt %d (exit %d)%s", envelope.Attempt, code, strings.TrimSpace(out))
	}
	return nil
}
