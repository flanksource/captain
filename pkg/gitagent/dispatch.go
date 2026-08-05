// Dispatch (§6.1): snapshot the supervisor's dirty worktree, record the task
// in the local mailbox, and push dispatch+control atomically to the agent's
// sidecar with the envelope riding on push options.
package gitagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TaskPayload is task.json: what the agent is asked to do. It is materialized
// outside the agent's worktree so it is not itself submittable (§4).
type TaskPayload struct {
	Prompt string `json:"prompt"`
	System string `json:"system,omitempty"`
	Model  string `json:"model,omitempty"`
	// Backend records which runtime the supervisor resolved, so the agent runs
	// the coding agent that was actually selected rather than re-resolving the
	// model name against its own defaults.
	Backend string `json:"backend,omitempty"`
}

// LoadTaskPayload reads a materialized task.json.
func LoadTaskPayload(path string) (TaskPayload, error) {
	var payload TaskPayload
	data, err := os.ReadFile(path)
	if err != nil {
		return payload, err
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return payload, fmt.Errorf("task file %s: %w", path, err)
	}
	if strings.TrimSpace(payload.Prompt) == "" {
		return payload, fmt.Errorf("task file %s carries no prompt", path)
	}
	return payload, nil
}

// TaskPaths locates a task's materialized inputs on a sidecar.
func TaskPaths(sidecarRepo, task string) (worktree, taskFile string, err error) {
	if err := ValidateTaskID(task); err != nil {
		return "", "", err
	}
	dir := taskStateDir(sidecarRepo, task)
	return filepath.Join(dir, "worktree"), filepath.Join(dir, ControlTaskFile), nil
}

// DispatchRequest carries one task hand-off.
type DispatchRequest struct {
	RepoDir       string
	MailboxPath   string
	Task          string // generated when empty
	Agent         string
	SidecarURL    string // ssh://host:port/repo.git
	SidecarHostFP string
	KeyPath       string
	SSHCommand    string // GIT_SSH_COMMAND; "" ⇒ this binary's git-agent ssh transport
	Relay         RelayMode
	Policy        Policy
	TaskPayload   TaskPayload
	HooksJSON     []byte // pre-serialized hooks.json (may be nil)
}

// DispatchResult reports the pushed hand-off.
type DispatchResult struct {
	Task     string
	Attempt  int
	Snapshot *Snapshot
	Control  string
}

// Dispatch performs §6.1 steps 1–3. The sidecar's admission and agent launch
// happen inside the push; the push returning is the proof of hand-off (H12).
func Dispatch(ctx context.Context, req DispatchRequest) (*DispatchResult, error) {
	task := req.Task
	if task == "" {
		var err error
		if task, err = NewTaskID(); err != nil {
			return nil, err
		}
	}
	if err := ValidateTaskID(task); err != nil {
		return nil, err
	}
	if req.Relay == "" {
		req.Relay = RelaySync
	}
	snapshot, err := TakeSnapshot(ctx, req.RepoDir, SnapshotPolicy{
		Paths: req.Policy.Paths, MaxFileSize: req.Policy.MaxBlobSize,
	})
	if err != nil {
		return nil, err
	}
	control, err := buildDispatchControl(ctx, req, snapshot)
	if err != nil {
		return nil, err
	}
	if err := recordDispatch(ctx, req, task, snapshot, control); err != nil {
		return nil, err
	}
	if err := pushDispatch(ctx, req, task, snapshot, control); err != nil {
		return nil, err
	}
	return &DispatchResult{Task: task, Attempt: 1, Snapshot: snapshot, Control: control}, nil
}

// NewTaskID mints a task id inside the §3.2 shape.
func NewTaskID() (string, error) {
	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "t-" + hex.EncodeToString(raw), nil
}

func buildDispatchControl(ctx context.Context, req DispatchRequest, snapshot *Snapshot) (string, error) {
	taskJSON, err := json.Marshal(req.TaskPayload)
	if err != nil {
		return "", err
	}
	policyJSON, err := json.Marshal(req.Policy)
	if err != nil {
		return "", err
	}
	hooksJSON := req.HooksJSON
	if len(hooksJSON) == 0 {
		hooksJSON = []byte("{}")
	}
	return BuildControlCommit(ctx, req.RepoDir, nil, map[string][]byte{
		ControlTaskFile:   taskJSON,
		ControlHooksFile:  hooksJSON,
		ControlPolicyFile: policyJSON,
	})
}

// recordDispatch writes the audit refs and task state into the local mailbox
// (R2.1): local update-refs, since the mailbox shares the real repository's
// objects through alternates.
func recordDispatch(ctx context.Context, req DispatchRequest, task string, snapshot *Snapshot, control string) error {
	if err := InitMailbox(ctx, req.MailboxPath, req.RepoDir); err != nil {
		return err
	}
	env := ScrubGitEnv(os.Environ())
	dispatchRef, err := DispatchRef(task, 1)
	if err != nil {
		return err
	}
	controlRef, err := ControlRef(task, 1)
	if err != nil {
		return err
	}
	if _, err := runGit(ctx, req.MailboxPath, env, "update-ref", dispatchRef, snapshot.Commit); err != nil {
		return err
	}
	if _, err := runGit(ctx, req.MailboxPath, env, "update-ref", controlRef, control); err != nil {
		return err
	}
	return SaveTaskState(req.MailboxPath, &TaskState{
		Task:           task,
		Agent:          req.Agent,
		Base:           snapshot.Base,
		DispatchCommit: snapshot.Commit,
		Relay:          req.Relay,
		Policy:         req.Policy,
	})
}

func pushDispatch(ctx context.Context, req DispatchRequest, task string, snapshot *Snapshot, control string) error {
	dispatchRef, err := DispatchRef(task, 1)
	if err != nil {
		return err
	}
	controlRef, err := ControlRef(task, 1)
	if err != nil {
		return err
	}
	envelope := Envelope{
		Version: ProtocolVersion,
		Task:    task,
		Attempt: 1,
		Base:    snapshot.Base,
		Depth:   0,
		Agent:   req.Agent,
		Relay:   req.Relay,
	}
	opts, err := envelope.Encode()
	if err != nil {
		return err
	}
	args := []string{"push", "--atomic"}
	for _, o := range opts {
		args = append(args, "--push-option="+o)
	}
	args = append(args, req.SidecarURL,
		snapshot.Commit+":"+dispatchRef,
		control+":"+controlRef,
	)
	pairs, err := transportPairs(req.SSHCommand, req.KeyPath, req.SidecarHostFP)
	if err != nil {
		return err
	}
	env := envWith(ScrubGitEnv(os.Environ()), pairs...)
	if _, err := runGit(ctx, req.RepoDir, env, args...); err != nil {
		return fmt.Errorf("dispatch push: %w", err)
	}
	return nil
}

// transportPairs builds the env for a push riding captain's GIT_SSH_COMMAND
// transport: no system ssh, key from a captain-managed path, host key pinned
// by fingerprint. An empty sshCommand means this binary's own transport.
func transportPairs(sshCommand, keyPath, hostFingerprint string) ([]string, error) {
	if sshCommand == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		sshCommand = exe + " sandbox git-agent ssh"
	}
	return []string{
		"GIT_SSH_COMMAND=" + sshCommand,
		"GIT_SSH_VARIANT=ssh", // an unrecognized command defaults to "simple", which cannot pass -p
		EnvSSHKey + "=" + keyPath,
		EnvSSHHostFingerprint + "=" + hostFingerprint,
	}, nil
}

// AwaitOutcome polls the mailbox for the task's final verdict: the first
// accepted one, or a rejection on the task's last permitted attempt, or a
// timeout. Rejected non-final attempts keep waiting — rejection is not
// termination (§6.3).
func AwaitOutcome(ctx context.Context, mailbox, task string, timeout time.Duration) (*TierVerdict, error) {
	if timeout <= 0 {
		timeout = time.Hour
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		st, ok, err := LoadTaskState(mailbox, task)
		if err != nil {
			return nil, err
		}
		maxAttempts := 0
		if ok {
			maxAttempts = st.Policy.MaxAttempts
		}
		for attempt := 1; attempt <= MaxAttempt; attempt++ {
			v, found, err := LoadVerdict(mailbox, task, attempt)
			if err != nil {
				return nil, err
			}
			if !found {
				break
			}
			if v.Status == StatusAccepted {
				return v, nil
			}
			if maxAttempts > 0 && attempt >= maxAttempts {
				return v, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, fmt.Errorf("task %s: no final verdict within %s", task, timeout)
		case <-tick.C:
		}
	}
}
