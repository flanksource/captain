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

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/clicky/text"
)

// TaskPayload is task.json: what the agent is asked to do. It is materialized
// outside the agent's worktree so it is not itself submittable (§4).
type TaskPayload struct {
	Prompt string `json:"prompt"`
	System string `json:"system,omitempty"`
	Model  string `json:"model,omitempty"`
	// Backend and Effort record the runtime the supervisor resolved, so the
	// agent does not re-resolve the model against its own defaults.
	Backend string     `json:"backend,omitempty"`
	Effort  api.Effort `json:"effort,omitempty"`
	// Timeout is the supervisor's effective deadline. The relocated runner
	// must not fall back to the shorter local model-call default.
	Timeout string `json:"timeout,omitempty"`
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
	MailboxRoute  string // opaque path under the supervisor's served root
	Task          string // generated when empty
	Agent         string
	SidecarURL    string // ssh://host:port/repo.git or https://host:port/git/repo.git
	SidecarHostFP string
	KeyPath       string
	SSHCommand    string // GIT_SSH_COMMAND; "" ⇒ this binary's git-agent ssh transport
	// Token, CAPath and PinnedPublicKey apply when SidecarURL is https://.
	Token           text.SensitiveString
	CAPath          string
	PinnedPublicKey string
	Relay           RelayMode
	Policy          Policy
	TaskPayload     TaskPayload
	HooksJSON       []byte // pre-serialized hooks.json (may be nil)
}

// Transport describes how to reach the sidecar, for whichever scheme its URL
// names.
func (r DispatchRequest) Transport() TransportTarget {
	return TransportTarget{
		URL: r.SidecarURL, SSHCommand: r.SSHCommand, KeyPath: r.KeyPath,
		HostFingerprint: r.SidecarHostFP,
		Token:           r.Token, CAPath: r.CAPath, PinnedPublicKey: r.PinnedPublicKey,
	}
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
	if err := ValidateMailboxRoute(req.MailboxRoute); err != nil {
		return nil, err
	}
	if req.Relay == "" {
		req.Relay = RelaySync
	}
	hooks, err := DecodeHookSets(req.HooksJSON)
	if err != nil {
		return nil, err
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
	if err := recordDispatch(ctx, req, task, snapshot, control, hooks); err != nil {
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
func recordDispatch(ctx context.Context, req DispatchRequest, task string, snapshot *Snapshot, control string, hooks *HookSets) error {
	if err := InitMailbox(ctx, req.MailboxPath, req.RepoDir); err != nil {
		return err
	}
	if err := copyDispatchObjects(ctx, req.RepoDir, req.MailboxPath, snapshot, control); err != nil {
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
	updates := fmt.Sprintf("start\ncreate %s %s\ncreate %s %s\nprepare\ncommit\n",
		dispatchRef, snapshot.Commit, controlRef, control)
	if _, err := runGitIn(ctx, req.MailboxPath, env, strings.NewReader(updates), "update-ref", "--stdin"); err != nil {
		return err
	}
	return SaveTaskState(req.MailboxPath, &TaskState{
		Task:           task,
		Agent:          req.Agent,
		Base:           snapshot.Base,
		DispatchCommit: snapshot.Commit,
		ControlCommit:  control,
		Relay:          req.Relay,
		Mailbox:        req.MailboxRoute,
		Policy:         req.Policy,
		Hooks:          hooks,
	})
}

// copyDispatchObjects gives the mailbox ownership of synthetic objects that
// have no ref in the real repository. The base history remains borrowed via
// alternates, while source-repository GC can no longer prune dispatch/control.
func copyDispatchObjects(ctx context.Context, source, mailbox string, snapshot *Snapshot, control string) error {
	packDir := filepath.Join(mailbox, "objects", "pack")
	if err := os.MkdirAll(packDir, 0o755); err != nil {
		return err
	}
	revisions := strings.NewReader(snapshot.Commit + "\n" + control + "\n^" + snapshot.Base + "\n")
	if _, err := runGitIn(ctx, source, ScrubGitEnv(os.Environ()), revisions,
		"pack-objects", "--revs", filepath.Join(packDir, "pack")); err != nil {
		return fmt.Errorf("storing dispatch objects in mailbox: %w", err)
	}
	return nil
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
		Mailbox: req.MailboxRoute,
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
	env, err := TransportEnv(ScrubGitEnv(os.Environ()), req.Transport())
	if err != nil {
		return err
	}
	if _, err := runGit(ctx, req.RepoDir, env, args...); err != nil {
		return fmt.Errorf("dispatch push: %w", err)
	}
	return nil
}

// executablePath is indirected so the SSH transport's default command can be
// resolved without every caller reaching for os.Executable.
var executablePath = os.Executable

// SSHTransportCommand renders this binary's SSH transport as a shell-safe
// GIT_SSH_COMMAND value. Git evaluates the value through a shell.
func SSHTransportCommand(executable string) string {
	return "'" + strings.ReplaceAll(executable, "'", "'\"'\"'") + "' sandbox git-agent ssh"
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
