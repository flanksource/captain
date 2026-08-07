// The nested relay (§6.2 step 9): still inside the sidecar's pre-receive, the
// vetted work is pushed onward to the supervisor's mailbox and the upstream
// sideband streams back out through the sidecar's own stderr. The relay lives
// in pre-receive because post-receive can no longer reject (R6.6/H16); it
// unsets GIT_QUARANTINE_PATH and keeps the inherited object directories, so
// no object is ever copied out of quarantine (R1.4).
package gitagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// RelayTarget is where and how the sidecar reaches the supervisor endpoint.
// The repository-specific mailbox route comes from trusted dispatch state.
type RelayTarget struct {
	URL             string `json:"url"`
	HostFingerprint string `json:"hostFingerprint"`
	KeyPath         string `json:"keyPath"`
	SSHCommand      string `json:"sshCommand,omitempty"` // "" ⇒ this binary's transport
}

// upstreamRejectedError distinguishes a supervisor verdict from a failure to
// obtain one. The supervisor has already persisted and rendered this verdict;
// the sidecar only needs the error to reject its own still-blocked push.
type upstreamRejectedError struct {
	verdict TierVerdict
}

func (e *upstreamRejectedError) Error() string {
	return fmt.Sprintf("supervisor rejected task %s attempt %d (%s)",
		e.verdict.Task, e.verdict.Attempt, e.verdict.Status)
}

const relayFeedbackTruncation = "captain: relay feedback truncated\n"

// relayFeedbackWriter removes the inner git client's "remote: " transport
// prefix before forwarding through the outer receive-pack. It also retains
// the structured supervisor verdict so an ordinary rejection is not
// misclassified as a sidecar transport error.
type relayFeedbackWriter struct {
	dst       io.Writer
	pending   string
	verdict   *TierVerdict
	dropping  bool
	truncated bool
}

func (w *relayFeedbackWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		if w.dropping {
			i := bytes.IndexByte(p, '\n')
			if i < 0 {
				return n, nil
			}
			p = p[i+1:]
			w.dropping = false
			continue
		}

		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			if len(p) > MaxFeedbackBytes-len(w.pending) {
				w.pending = ""
				w.dropping = true
				if err := w.writeTruncation(); err != nil {
					return 0, err
				}
				return n, nil
			}
			w.pending += string(p)
			return n, nil
		}

		fragment := p[:i+1]
		p = p[i+1:]
		if len(fragment) > MaxFeedbackBytes-len(w.pending) {
			w.pending = ""
			if err := w.writeTruncation(); err != nil {
				return 0, err
			}
			continue
		}
		w.pending += string(fragment)
		line := w.pending
		w.pending = ""
		if err := w.writeLine(line); err != nil {
			return 0, err
		}
	}
	return n, nil
}

func (w *relayFeedbackWriter) writeTruncation() error {
	if w.truncated {
		return nil
	}
	w.truncated = true
	_, err := io.WriteString(w.dst, relayFeedbackTruncation)
	return err
}

func (w *relayFeedbackWriter) flush() error {
	if w.pending == "" {
		return nil
	}
	line := w.pending
	w.pending = ""
	return w.writeLine(line)
}

func (w *relayFeedbackWriter) writeLine(line string) error {
	line = strings.TrimPrefix(line, "remote: ")
	candidate := strings.TrimSpace(line)
	if raw, ok := strings.CutPrefix(candidate, "captain-json: "); ok {
		var verdict TierVerdict
		if json.Unmarshal([]byte(raw), &verdict) == nil && verdict.Tier == "supervisor" {
			w.verdict = &verdict
		}
	}
	_, err := io.WriteString(w.dst, line)
	return err
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
func Relay(ctx context.Context, repo string, hookEnv []string, target RelayTarget, mailboxRoute string, envelope Envelope, result, control string, sideband io.Writer) error {
	mailboxURL, err := MailboxURL(target.URL, mailboxRoute)
	if err != nil {
		return err
	}
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
	args = append(args, mailboxURL, result+":"+resultRef, control+":"+controlRef)

	pairs, err := transportPairs(target.SSHCommand, target.KeyPath, target.HostFingerprint)
	if err != nil {
		return err
	}
	// R1.4: unset only GIT_QUARANTINE_PATH; the object-directory variables
	// stay so the quarantined objects remain readable for the outbound pack.
	env := envWith(RelayEnv(hookEnv), pairs...)
	feedback := &relayFeedbackWriter{dst: sideband}
	code, out, err := gitExitCodeStderr(ctx, repo, env, feedback, args...)
	if flushErr := feedback.flush(); err == nil && flushErr != nil {
		err = flushErr
	}
	if err != nil {
		return err
	}
	if code != 0 {
		if feedback.verdict != nil && feedback.verdict.Rejects() {
			return &upstreamRejectedError{verdict: *feedback.verdict}
		}
		return fmt.Errorf("supervisor rejected attempt %d (exit %d)%s", envelope.Attempt, code, strings.TrimSpace(out))
	}
	return nil
}
