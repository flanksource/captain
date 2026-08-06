// The receive-hook entrypoints the shims exec (§6.2). pre-receive is the only
// place a push can be rejected, so admission, hook set execution and the
// nested relay all live there (R6.6); post-receive owns everything that needs
// quarantine to have ended — workspace setup, agent launch, integration
// (R6.4).
package gitagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent/verify"
	"github.com/flanksource/captain/pkg/api"
)

// HookRuntime is the serializable part of a receiver's hook configuration.
type HookRuntime struct {
	SidecarWorkflow    *api.Workflow `json:"sidecarWorkflow,omitempty"`
	SupervisorWorkflow *api.Workflow `json:"supervisorWorkflow,omitempty"`
	// HookSandbox names the wrap-command sandbox confining exec hooks (R5.2):
	// srt or container. The value test-identity is accepted only inside a Go
	// test binary and cannot activate in production.
	HookSandbox  string      `json:"hookSandbox,omitempty"`
	AgentCommand string      `json:"agentCommand,omitempty"`
	RealRepo     string      `json:"realRepo,omitempty"` // mailbox-local integration target
	Relay        RelayTarget `json:"relay,omitempty"`    // sidecar: supervisor base endpoint
}

// RequiresJudge reports whether either receiver tier declares prompt checks.
func (r HookRuntime) RequiresJudge() bool {
	for _, workflow := range []*api.Workflow{r.SidecarWorkflow, r.SupervisorWorkflow} {
		if workflow != nil && workflow.Verify != nil && len(workflow.Verify.Prompts) > 0 {
			return true
		}
	}
	return false
}

// HookHost is a runtime plus the process-local collaborators a hook set needs.
type HookHost struct {
	Runtime HookRuntime
	Judge   ai.Provider
	WrapFor HookWrapFactory
	Timeout time.Duration
	// DefaultAgentCommand supplies the command to launch when the backend
	// declares no agentCommand. Without it a dispatch would prepare a
	// workspace nothing ever works on, and the supervisor would wait out its
	// whole budget in silence. The task id is only known here, at
	// post-receive, which is why this is a builder rather than a string.
	DefaultAgentCommand func(repo, task string) string
}

// agentCommandFor resolves what to launch for a task: the configured command,
// else the host's default.
func (h HookHost) agentCommandFor(repo, task string) string {
	if command := strings.TrimSpace(h.Runtime.AgentCommand); command != "" {
		return command
	}
	if h.DefaultAgentCommand != nil {
		return h.DefaultAgentCommand(repo, task)
	}
	return ""
}

// LoadHookRuntime reads a HookRuntime JSON file; an empty path is an empty
// runtime.
func LoadHookRuntime(path string) (HookRuntime, error) {
	var rt HookRuntime
	if path == "" {
		return rt, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return rt, err
	}
	if err := json.Unmarshal(data, &rt); err != nil {
		return rt, fmt.Errorf("hook runtime %s: %w", path, err)
	}
	return rt, nil
}

// HookWrapFactory builds a confinement wrapper scoped to one materialized
// workspace. The sandbox's filesystem policy depends on the tree it confines,
// and that tree only exists once a push is being vetted — so hooks resolve a
// factory up front and construct the wrapper per vet, after materialization.
// The returned close releases the sandbox; callers must invoke it once the
// hook set has run.
type HookWrapFactory func(ctx context.Context, dir string) (verify.CommandWrapFunc, func() error, error)

// ResolveHookWrap maps HookRuntime.HookSandbox onto a per-workspace
// confinement factory via the sandbox registry. Empty means none — RunHookSet
// then refuses exec hooks rather than running them bare (R5.2). repo is the
// receiving repository, which the hook policy must hide from the confined
// command. Unknown kinds and adapters without a command wrapper fail here, at
// hook startup, not mid-vet.
func ResolveHookWrap(name, repo string) (HookWrapFactory, error) {
	switch name {
	case "":
		return nil, nil
	case "test-identity":
		if !testing.Testing() {
			return nil, fmt.Errorf("hookSandbox test-identity is only available inside a test binary (R5.2)")
		}
		return func(_ context.Context, _ string) (verify.CommandWrapFunc, func() error, error) {
			wrap := func(_ context.Context, cmd string, args, env []string) (string, []string, []string, error) {
				return cmd, args, env, nil
			}
			return wrap, func() error { return nil }, nil
		}, nil
	}
	kind, ok := api.ParseSandboxKind(name)
	if !ok {
		return nil, fmt.Errorf("unknown hook sandbox kind %q", name)
	}
	cfg := api.SandboxConfig{Kind: kind, Options: map[string]any{
		api.SandboxOptionProfile: api.SandboxProfileHook,
	}}
	if abs, err := filepath.Abs(repo); err == nil && repo != "" {
		cfg.Options[api.SandboxOptionDenyRead] = []string{abs}
	}
	// Probe once at resolve time so a kind that cannot confine commands is a
	// startup error rather than a per-push surprise.
	probe, err := api.NewSandbox(cfg)
	if err != nil {
		return nil, err
	}
	_, canWrap := api.SandboxAs[api.CommandWrapper](probe)
	_ = probe.Close()
	if !canWrap {
		return nil, fmt.Errorf("hook sandbox %q provides no command wrapper; exec hooks cannot run confined (R5.2)", name)
	}
	return func(ctx context.Context, dir string) (verify.CommandWrapFunc, func() error, error) {
		sandbox, err := api.NewSandbox(cfg)
		if err != nil {
			return nil, nil, err
		}
		spec := &api.Spec{}
		spec.SetCwd(dir)
		if _, err := sandbox.Prepare(ctx, spec); err != nil {
			_ = sandbox.Close()
			return nil, nil, err
		}
		wrapper, ok := api.SandboxAs[api.CommandWrapper](sandbox)
		if !ok {
			_ = sandbox.Close()
			return nil, nil, fmt.Errorf("hook sandbox %q provides no command wrapper; exec hooks cannot run confined (R5.2)", name)
		}
		return wrapper.Wrap, sandbox.Close, nil
	}, nil
}

// HookMain is the shim entrypoint: args are
// <pre-receive|post-receive> <repo> <role> [runtime-config.json].
func HookMain(args []string) int {
	if len(args) < 3 || len(args) > 4 {
		fmt.Fprintln(os.Stderr, "captain: usage: hook <pre-receive|post-receive> <repo> <role> [runtime.json]")
		return 1
	}
	hook, repo, role := args[0], args[1], args[2]
	cfgPath := ""
	if len(args) == 4 {
		cfgPath = args[3]
	}
	runtime, err := LoadHookRuntime(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "captain: %v\n", err)
		return 1
	}
	if runtime.RequiresJudge() {
		fmt.Fprintln(os.Stderr, "captain: verify prompts declared but the standalone hook shim has no provider; use `captain sandbox git-agent hook`")
		return 1
	}
	wrapFor, err := ResolveHookWrap(runtime.HookSandbox, repo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "captain: %v\n", err)
		return 1
	}
	host := HookHost{Runtime: runtime, WrapFor: wrapFor}
	ctx := context.Background()
	switch hook {
	case "pre-receive":
		if err := RunPreReceive(ctx, repo, ReceiverRole(role), host, os.Stdin, os.Stderr); err != nil {
			return 1
		}
		return 0
	case "post-receive":
		if err := RunPostReceive(ctx, repo, ReceiverRole(role), host, os.Stdin); err != nil {
			// post-receive exit status cannot reject; still surface the problem.
			fmt.Fprintf(os.Stderr, "captain: post-receive: %v\n", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "captain: unknown hook %q\n", hook)
		return 1
	}
}

// RunPreReceive is the admission + vetting + relay tier. A non-nil error
// means the push is rejected; the reason has already been written to
// sideband.
func RunPreReceive(ctx context.Context, repo string, role ReceiverRole, host HookHost, stdin io.Reader, sideband io.Writer) error {
	updates, err := ParseRefUpdates(stdin)
	if err != nil {
		fmt.Fprintf(sideband, "captain: %v\n", err)
		return err
	}
	envelope := envelopeFromHookEnv()
	req := AdmitRequest{Repo: repo, Role: role, Agent: os.Getenv(EnvAgentName), Updates: updates, Envelope: envelope, Env: os.Environ()}
	if err := Admit(ctx, req); err != nil {
		fmt.Fprintf(sideband, "captain: REJECTED (admission)\ncaptain: %v\n", err)
		return err
	}
	if role == RoleSidecar {
		return sidecarPreReceive(ctx, repo, host, updates, sideband)
	}
	return mailboxPreReceive(ctx, repo, host, updates, envelope, sideband)
}

func envelopeFromHookEnv() *Envelope {
	envelope, err := EnvelopeFromEnv(os.Getenv)
	if err != nil {
		return nil // an agent's bare push carries none; Admit enforces per-ref
	}
	return &envelope
}

// sidecarPreReceive vets an agent's submit. Dispatch pushes (protocol refs
// only) were fully admitted above and need no hook set — theirs runs on
// submit.
func sidecarPreReceive(ctx context.Context, repo string, host HookHost, updates []RefUpdate, sideband io.Writer) error {
	branchUpdate, task, ok := singleAgentBranchUpdate(updates)
	if !ok {
		return nil
	}
	var attempt int
	st, err := UpdateTaskState(repo, task, func(current *TaskState) (bool, error) {
		attempt = current.Attempts + 1
		if current.Policy.MaxAttempts > 0 && attempt > current.Policy.MaxAttempts {
			return false, nil
		}
		current.Attempts = attempt
		return true, nil
	})
	if err != nil {
		fmt.Fprintf(sideband, "captain: %v\n", err)
		return err
	}
	if st == nil {
		fmt.Fprintf(sideband, "captain: task state missing for %s\n", task)
		return fmt.Errorf("task state missing for %s", task)
	}
	// An attempt is consumed per submit, rejected or not (§6.3): the retry
	// after a rejection is attempt n+1.
	if st.Policy.MaxAttempts > 0 && attempt > st.Policy.MaxAttempts {
		verdict := TierVerdict{
			V: ProtocolVersion, Task: task, Attempt: attempt, Tier: string(RoleSidecar), Status: StatusRejected,
			Findings: []Finding{{Hook: "gate:max-attempts", Kind: "commit",
				Message: fmt.Sprintf("attempt %d exceeds the task's maxAttempts %d", attempt, st.Policy.MaxAttempts)}},
		}
		return rejectWithVerdict(repo, verdict, sideband)
	}
	workflow := host.Runtime.SidecarWorkflow
	if st.Hooks != nil && st.Hooks.Sidecar != nil {
		workflow = st.Hooks.Sidecar
	}
	verdict := vetTree(ctx, repo, vetRequest{
		host: host, workflow: workflow, tier: string(RoleSidecar),
		task: task, attempt: attempt, depth: 0,
		from: st.DispatchCommit, to: branchUpdate.New,
	})
	if verdict.Rejects() {
		return rejectWithVerdict(repo, verdict, sideband)
	}
	if host.Runtime.Relay.URL != "" {
		if err := relayUpward(ctx, repo, host, st, attempt, branchUpdate.New, sideband); err != nil {
			var rejected *upstreamRejectedError
			if errors.As(err, &rejected) {
				// The supervisor already persisted and rendered the authoritative
				// verdict. Return non-zero to reject this push without replacing it
				// with a second, generic sidecar relay-error verdict.
				return err
			}
			verdict.Status = StatusError
			verdict.Findings = append(verdict.Findings, Finding{
				Hook: "relay", Kind: "exec", Message: err.Error(),
			})
			return rejectWithVerdict(repo, verdict, sideband)
		}
	}
	if err := SaveVerdict(repo, verdict); err != nil {
		fmt.Fprintf(sideband, "captain: %v\n", err)
		return err
	}
	return WriteFeedback(sideband, verdict, "")
}

// relayUpward pushes the squashed result plus the ORIGINAL dispatch control
// commit at the attempt's control ref. On attempt 1 the mailbox already holds
// that exact commit from the supervisor's audit write, so git filters the
// control update as up-to-date; on a retry it is a fresh create — either way
// R3.2's create-only holds and the R3.4 pairing invariant is preserved.
func relayUpward(ctx context.Context, repo string, host HookHost, st *TaskState, attempt int, tip string, sideband io.Writer) error {
	hookEnv := os.Environ()
	result, err := BuildResultCommit(ctx, repo, hookEnv, tip, st.DispatchCommit)
	if err != nil {
		return err
	}
	control := st.ControlCommit
	if control == "" {
		return fmt.Errorf("task %s has no recorded control commit; cannot relay", st.Task)
	}
	if err := ValidateMailboxRoute(st.Mailbox); err != nil {
		return fmt.Errorf("task %s has no usable mailbox route: %w", st.Task, err)
	}
	envelope := Envelope{
		Version: ProtocolVersion, Task: st.Task, Attempt: attempt,
		Base: st.Base, Depth: 0, Agent: st.Agent, Relay: st.Relay,
	}
	return Relay(ctx, repo, hookEnv, host.Runtime.Relay, st.Mailbox, envelope, result, control, sideband)
}

// mailboxPreReceive runs hook set #2 over an arriving result (§6.2 step 10).
// The accept-path verdict is written by post-receive after integration; only
// a rejection must persist here, before the non-zero exit (R6.9).
func mailboxPreReceive(ctx context.Context, repo string, host HookHost, updates []RefUpdate, envelope *Envelope, sideband io.Writer) error {
	resultUpdate, info, ok := singleResultUpdate(updates)
	if !ok {
		return nil
	}
	st, found, err := LoadTaskState(repo, info.Task)
	if err != nil || !found {
		fmt.Fprintf(sideband, "captain: task state missing for %s\n", info.Task)
		return fmt.Errorf("task state missing for %s", info.Task)
	}
	depth := 0
	if envelope != nil {
		depth = envelope.Depth
	}
	workflow := host.Runtime.SupervisorWorkflow
	if st.Hooks != nil && st.Hooks.Supervisor != nil {
		workflow = st.Hooks.Supervisor
	}
	verdict := vetTree(ctx, repo, vetRequest{
		host: host, workflow: workflow, tier: "supervisor",
		task: info.Task, attempt: info.Attempt, depth: depth,
		from: st.DispatchCommit, to: resultUpdate.New,
	})
	if verdict.Rejects() {
		return rejectWithVerdict(repo, verdict, sideband)
	}
	if host.Runtime.RealRepo != "" {
		conflict, err := checkIntegration(ctx, host.Runtime.RealRepo, repo, st.Base, resultUpdate.New, os.Environ())
		if err != nil {
			verdict.Status = StatusError
			verdict.Findings = append(verdict.Findings, Finding{
				Hook: "integrate", Kind: "commit", Message: "could not evaluate integration: " + err.Error(),
			})
			return rejectWithVerdict(repo, verdict, sideband)
		}
		if conflict != "" {
			verdict.Status = StatusRejected
			verdict.Findings = append(verdict.Findings, Finding{
				Hook: "integrate", Kind: "commit", Message: conflict,
			})
			return rejectWithVerdict(repo, verdict, sideband)
		}
	}
	return nil
}

type vetRequest struct {
	host     HookHost
	workflow *api.Workflow
	tier     string
	task     string
	attempt  int
	depth    int
	from, to string
}

// vetTree materializes the pushed tree and runs one tier's hook set over it.
// Every failure mode folds into the verdict (R7.5).
func vetTree(ctx context.Context, repo string, req vetRequest) TierVerdict {
	verdict := TierVerdict{V: ProtocolVersion, Task: req.task, Attempt: req.attempt, Tier: req.tier, Status: StatusError}
	dir, err := os.MkdirTemp("", "captain-vet-")
	if err != nil {
		verdict.Findings = append(verdict.Findings, Finding{Hook: "materialize", Kind: "exec", Message: err.Error()})
		return verdict
	}
	defer os.RemoveAll(dir)
	hookEnv := os.Environ()
	if _, err := Materialize(ctx, repo, hookEnv, req.to, dir); err != nil {
		verdict.Findings = append(verdict.Findings, Finding{Hook: "materialize", Kind: "exec", Message: err.Error()})
		return verdict
	}
	changed, err := changedPathsBetween(ctx, repo, hookEnv, req.from, req.to)
	if err != nil {
		verdict.Findings = append(verdict.Findings, Finding{Hook: "materialize", Kind: "exec", Message: err.Error()})
		return verdict
	}
	// The confinement wrapper is built here, against the tree it will confine:
	// only now does the workspace exist, and a sandbox whose filesystem policy
	// was computed for any other directory would confine the wrong thing. A
	// factory failure is fail-closed (R5.2): status error, and error rejects.
	var wrap verify.CommandWrapFunc
	if req.host.WrapFor != nil && len(verify.HooksForWorkflow(req.workflow)) > 0 {
		wrapped, closeWrap, err := req.host.WrapFor(ctx, dir)
		if err != nil {
			verdict.Findings = append(verdict.Findings, Finding{Hook: "hookset", Kind: "exec",
				Message: "hook sandbox could not confine the workspace: " + err.Error()})
			return verdict
		}
		defer func() { _ = closeWrap() }()
		wrap = wrapped
	}
	stop := StartProgress(os.Stderr, req.tier+" hooks", 30*time.Second)
	defer stop()
	return RunHookSet(ctx, HookWorkspace{Dir: dir, Changed: changed}, HookSetOptions{
		Workflow: req.workflow,
		Tier:     req.tier,
		Task:     req.task,
		Attempt:  req.attempt,
		Depth:    req.depth,
		Judge:    req.host.Judge,
		Wrap:     wrap,
		Env:      HookExecEnv(hookEnv),
		Timeout:  req.host.Timeout,
	})
}

// rejectWithVerdict persists the verdict out-of-band before the non-zero exit
// (R6.9), then writes the sideband feedback block (§7).
func rejectWithVerdict(repo string, verdict TierVerdict, sideband io.Writer) error {
	logPath := verdictPath(repo, verdict.Task, verdict.Attempt)
	if err := SaveVerdict(repo, verdict); err != nil {
		fmt.Fprintf(sideband, "captain: persisting verdict: %v\n", err)
	}
	_ = WriteFeedback(sideband, verdict, logPath)
	return fmt.Errorf("push rejected: task %s attempt %d (%s)", verdict.Task, verdict.Attempt, verdict.Tier)
}

// RunPostReceive owns the work that is only legal once quarantine has ended
// (R6.4): sidecar workspace setup and agent launch on dispatch, supervisor
// integration and the verdict ref on an accepted result.
func RunPostReceive(ctx context.Context, repo string, role ReceiverRole, host HookHost, stdin io.Reader) error {
	updates, err := ParseRefUpdates(stdin)
	if err != nil {
		return err
	}
	envelope := envelopeFromHookEnv()
	if role == RoleSidecar {
		return sidecarPostReceive(ctx, repo, host, updates, envelope)
	}
	return mailboxPostReceive(ctx, repo, host, updates)
}

func sidecarPostReceive(ctx context.Context, repo string, host HookHost, updates []RefUpdate, envelope *Envelope) error {
	for _, u := range updates {
		if !IsProtocolRef(u.Ref) {
			continue
		}
		info, err := ParseTaskRef(u.Ref)
		if err != nil || info.Kind != RefDispatch {
			continue
		}
		if envelope == nil {
			return fmt.Errorf("dispatch ref %s arrived without an envelope", u.Ref)
		}
		payloads, err := loadDispatchPayloads(ctx, repo, updates, info)
		if err != nil {
			return err
		}
		if err := SaveTaskState(repo, &TaskState{
			Task: info.Task, Agent: envelope.Agent, Base: envelope.Base,
			DispatchCommit: u.New, ControlCommit: payloads.controlCommit,
			Relay: envelope.Relay, Mailbox: envelope.Mailbox,
			Policy: payloads.policy, Hooks: payloads.hooks,
		}); err != nil {
			return err
		}
		workdir, err := SetupAgentWorkspace(ctx, repo, info.Task, u.New)
		if err != nil {
			return err
		}
		taskFile, err := WriteTaskFile(repo, info.Task, payloads.task)
		if err != nil {
			return err
		}
		if err := LaunchAgent(repo, info.Task, workdir, taskFile, host.agentCommandFor(repo, info.Task)); err != nil {
			return err
		}
	}
	return nil
}

type dispatchPayloads struct {
	policy        Policy
	task          []byte
	hooks         *HookSets
	controlCommit string
}

// loadDispatchPayloads reads the task policy carried by the paired control
// commit. Hooks become durable receiver state; consulting only local receiver
// config would let the dispatched vetting policy disappear at the sidecar.
func loadDispatchPayloads(ctx context.Context, repo string, updates []RefUpdate, dispatch RefInfo) (dispatchPayloads, error) {
	payloads := dispatchPayloads{task: []byte("{}")}
	controlRef, err := ControlRef(dispatch.Task, dispatch.Attempt)
	if err != nil {
		return payloads, err
	}
	control := refUpdateFor(updates, controlRef)
	if control.New == "" || isZeroOID(control.New) {
		return payloads, fmt.Errorf("dispatch %s has no paired control commit", dispatch.Task)
	}
	payloads.controlCommit = control.New
	env := os.Environ()
	if raw, err := ReadControlPayload(ctx, repo, env, control.New, ControlPolicyFile); err == nil {
		if err := json.Unmarshal(raw, &payloads.policy); err != nil {
			return payloads, fmt.Errorf("decode %s: %w", ControlPolicyFile, err)
		}
	}
	if raw, err := ReadControlPayload(ctx, repo, env, control.New, ControlTaskFile); err == nil && len(raw) > 0 {
		payloads.task = raw
	}
	if raw, err := ReadControlPayload(ctx, repo, env, control.New, ControlHooksFile); err == nil {
		payloads.hooks, err = DecodeHookSets(raw)
		if err != nil {
			return payloads, err
		}
	}
	return payloads, nil
}

func mailboxPostReceive(ctx context.Context, repo string, host HookHost, updates []RefUpdate) error {
	resultUpdate, info, ok := singleResultUpdate(updates)
	if !ok {
		return nil
	}
	st, found, err := LoadTaskState(repo, info.Task)
	if err != nil || !found {
		return fmt.Errorf("task state missing for %s", info.Task)
	}
	verdict := TierVerdict{V: ProtocolVersion, Task: info.Task, Attempt: info.Attempt, Tier: "supervisor", Status: StatusAccepted}
	if host.Runtime.RealRepo != "" {
		// Admission already proved that the envelope agrees with this durable
		// state. Keep the authoritative dispatch record as the sole source for
		// the three-way merge base (R10.1).
		integration, err := Integrate(ctx, host.Runtime.RealRepo, repo, info.Task, info.Attempt, st.Base, resultUpdate.New)
		if err != nil {
			return err
		}
		if integration.Conflict != "" {
			// Pre-receive normally rejects this. If HEAD changed in the narrow
			// gap before post-receive, fail the verdict closed instead of
			// claiming that unintegrated work was accepted.
			verdict.Status = StatusError
			verdict.Findings = append(verdict.Findings, Finding{
				Hook: "integrate", Kind: "commit", Message: integration.Conflict,
			})
		} else {
			verdict.Findings = append(verdict.Findings, Finding{
				Hook: "integrate", Kind: "commit",
				Message: "merged onto " + integration.Branch, Path: integration.Branch,
			})
		}
	}
	if _, err := UpdateTaskState(repo, info.Task, func(current *TaskState) (bool, error) {
		if info.Attempt > current.Attempts {
			current.Attempts = info.Attempt
		}
		return true, nil
	}); err != nil {
		return err
	}
	if err := SaveVerdict(repo, verdict); err != nil {
		return err
	}
	return writeVerdictRef(ctx, repo, verdict)
}

// writeVerdictRef records the verdict as a control commit on
// refs/captain/tasks/<task>/verdict/<attempt> (§3.2).
func writeVerdictRef(ctx context.Context, repo string, verdict TierVerdict) error {
	ref, err := VerdictRef(verdict.Task, verdict.Attempt)
	if err != nil {
		return err
	}
	payload, err := json.MarshalIndent(verdict, "", "  ")
	if err != nil {
		return err
	}
	commit, err := BuildControlCommit(ctx, repo, nil, map[string][]byte{"verdict.json": payload})
	if err != nil {
		return err
	}
	_, err = runGit(ctx, repo, os.Environ(), "update-ref", ref, commit)
	return err
}

func singleAgentBranchUpdate(updates []RefUpdate) (RefUpdate, string, bool) {
	for _, u := range updates {
		if strings.HasPrefix(u.Ref, agentBranchPrefix) {
			return u, strings.TrimPrefix(u.Ref, agentBranchPrefix), true
		}
	}
	return RefUpdate{}, "", false
}

func singleResultUpdate(updates []RefUpdate) (RefUpdate, RefInfo, bool) {
	for _, u := range updates {
		if info, err := ParseTaskRef(u.Ref); err == nil && info.Kind == RefResult {
			return u, info, true
		}
	}
	return RefUpdate{}, RefInfo{}, false
}

// changedPathsBetween lists the paths differing between two commits.
func changedPathsBetween(ctx context.Context, repo string, env []string, from, to string) ([]string, error) {
	out, err := runGitRaw(ctx, repo, env, nil,
		"diff-tree", "-r", "-z", "--name-only", "--no-commit-id", "--no-renames", from, to)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}
