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
	"os"
	"strings"
)

// RelayTarget is where and how the sidecar reaches the supervisor endpoint.
// The repository-specific mailbox route comes from trusted dispatch state.
type RelayTarget struct {
	URL             string `json:"url"`
	HostFingerprint string `json:"hostFingerprint"`
	KeyPath         string `json:"keyPath"`
	SSHCommand      string `json:"sshCommand,omitempty"` // "" ⇒ this binary's transport
	// TokenPath, CAPath and PinnedPublicKey apply when URL is https://.
	//
	// A path rather than the credential itself, exactly as KeyPath is: this
	// struct is serialized into hooks.json, which travels between hosts and is
	// readable by every hook process. The sidecar already holds its own token
	// from enrollment, so there is nothing to send it.
	TokenPath       string `json:"tokenPath,omitempty"`
	CAPath          string `json:"caPath,omitempty"`
	PinnedPublicKey string `json:"pinnedPubkey,omitempty"`
}

// Transport describes how to reach pushURL, which is the target's endpoint
// joined with one repository's mailbox route. The token is read at the moment
// of use so a revoked-and-reissued credential takes effect without a redeploy.
func (t RelayTarget) Transport(pushURL string) (TransportTarget, error) {
	target := TransportTarget{
		URL: pushURL, SSHCommand: t.SSHCommand, KeyPath: t.KeyPath,
		HostFingerprint: t.HostFingerprint,
		CAPath:          t.CAPath, PinnedPublicKey: t.PinnedPublicKey,
	}
	if EndpointScheme(pushURL) != "https" {
		return target, nil
	}
	token, err := ReadTokenFile(t.TokenPath)
	if err != nil {
		return TransportTarget{}, err
	}
	target.Token = token
	return target, nil
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

	transport, err := target.Transport(mailboxURL)
	if err != nil {
		return err
	}
	// R1.4: unset only GIT_QUARANTINE_PATH; the object-directory variables
	// stay so the quarantined objects remain readable for the outbound pack.
	env, err := TransportEnv(RelayEnv(hookEnv), transport)
	if err != nil {
		return err
	}
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

// ReportTaskFailure sends a terminal error verdict when the detached worker
// exits before it can make the ordinary branch push that starts result relay.
func ReportTaskFailure(ctx context.Context, repo string, target RelayTarget, task string, failure error) error {
	if failure == nil {
		return fmt.Errorf("task failure is required")
	}
	var attempt int
	st, err := UpdateTaskState(repo, task, func(current *TaskState) (bool, error) {
		attempt = current.Attempts + 1
		if current.Policy.MaxAttempts > 0 && attempt > current.Policy.MaxAttempts {
			return false, fmt.Errorf("attempt %d exceeds the task's maxAttempts %d", attempt, current.Policy.MaxAttempts)
		}
		current.Attempts = attempt
		return true, nil
	})
	if err != nil {
		return err
	}
	if st == nil {
		return fmt.Errorf("task %s state is missing", task)
	}
	message := failure.Error()
	if len(message) > maxFindingFeedback {
		message = message[:maxFindingFeedback] + "\n[failure message truncated]"
	}
	verdict := TierVerdict{
		V: ProtocolVersion, Task: task, Attempt: attempt,
		Tier: string(RoleSidecar), Status: StatusError, Terminal: true,
		Findings: []Finding{{Hook: "agent", Kind: "exec", Message: message}},
	}
	if err := SaveVerdict(repo, verdict); err != nil {
		return fmt.Errorf("save local failure verdict: %w", err)
	}
	payload, err := json.MarshalIndent(verdict, "", "  ")
	if err != nil {
		return err
	}
	commit, err := BuildControlCommit(ctx, repo, nil, map[string][]byte{ControlVerdictFile: payload})
	if err != nil {
		return err
	}
	mailboxURL, err := MailboxURL(target.URL, st.Mailbox)
	if err != nil {
		return err
	}
	verdictRef, err := VerdictRef(task, attempt)
	if err != nil {
		return err
	}
	envelope := Envelope{
		Version: ProtocolVersion, Task: task, Attempt: attempt,
		Base: st.Base, Depth: 0, Agent: st.Agent, Relay: st.Relay,
	}
	opts, err := envelope.Encode()
	if err != nil {
		return err
	}
	args := []string{"push", "--atomic"}
	for _, option := range opts {
		args = append(args, "--push-option="+option)
	}
	args = append(args, mailboxURL, commit+":"+verdictRef)
	transport, err := target.Transport(mailboxURL)
	if err != nil {
		return err
	}
	env, err := TransportEnv(ScrubGitEnv(os.Environ()), transport)
	if err != nil {
		return err
	}
	if _, err := runGit(ctx, repo, env, args...); err != nil {
		return fmt.Errorf("relay terminal failure verdict: %w", err)
	}
	return nil
}
