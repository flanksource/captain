// Package substraterun is the substrate sandbox: it runs a whole prompt run in an agent-substrate
// environment of its own and brings the run's commits back as a branch. A host that offers it
// imports this package, which registers the kind.
package substraterun

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/flanksource/captain/pkg/promptrun"
	"github.com/flanksource/captain/pkg/promptrun/remoterun"
	"github.com/flanksource/captain/pkg/substrate"
	"github.com/flanksource/commons/logger"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The run's directory in its environment, and what the two sides of the run leave in it. The
// environment's program runs in RunDir, so the other paths are relative to it.
const (
	RunDir = "/tmp/captain-run"
	// TaskFile holds the Task.
	TaskFile = "task.json"
	// WorkspaceBundle carries the caller's checkout, uncommitted work included, as Task.Ref.
	WorkspaceBundle = "in.bundle"
	// CommitsBundle is what the run committed, as HEAD on top of Task.Ref. The program writes it
	// before it sends the run's result, and leaves it out when the run committed nothing.
	CommitsBundle = "out/commits.bundle"
)

// Task is what the environment's program is handed: the run, and the ref the workspace bundle
// carries the caller's checkout as.
type Task struct {
	Job remoterun.Job `json:"job"`
	Ref string        `json:"ref"`
}

// Substrate constructs the substrate sandbox from a backend's options. It reaches no cluster:
// each run dials its own connection.
func Substrate(cfg api.SandboxConfig) (api.Sandbox, error) {
	options, err := decodeOptions(cfg)
	if err != nil {
		return nil, err
	}
	return &substrateSandbox{options: options}, nil
}

func init() {
	api.RegisterSandbox(api.SandboxSubstrate, Substrate)
}

type substrateSandbox struct {
	options options
}

func (s *substrateSandbox) Kind() api.SandboxKind { return api.SandboxSubstrate }

func (s *substrateSandbox) Prepare(context.Context, *api.Spec) (*api.SandboxSession, error) {
	return &api.SandboxSession{}, nil
}

func (s *substrateSandbox) Close() error { return nil }

// IsolatesWorkspace: the run happens in the environment's own checkout.
func (s *substrateSandbox) IsolatesWorkspace() bool { return true }

// ProvidesEgressProxy: the environment holds placeholders; substrate's egress gateway puts the
// real keys into its requests.
func (s *substrateSandbox) ProvidesEgressProxy() bool { return true }

// RunRemote runs in in an environment created for it, from the caller's checkout as it is, and
// fetches what the run committed into the checkout's repository as captain/substrate/<run>. The
// environment is deleted however the run ends.
func (s *substrateSandbox) RunRemote(ctx context.Context, in promptrun.Input, started func(api.Spec) error) (promptrun.Result, error) {
	spec := in.Resolved.Spec
	if len(in.Hooks) > 0 && len(in.HostData) == 0 {
		return promptrun.Result{}, errors.New("sandbox substrate runs the whole run in its environment, where the caller's hooks can't run; set Input.HostData for the environment's program to rebuild them from")
	}
	if spec.SessionID != "" {
		return promptrun.Result{}, fmt.Errorf("sandbox substrate can't resume session %s: the run's environment starts without the agent's earlier sessions", spec.SessionID)
	}
	run, err := runID()
	if err != nil {
		return promptrun.Result{}, err
	}
	lifetime := s.options.maxLifetime
	if deadline, ok := ctx.Deadline(); ok {
		if lasts := time.Until(deadline).Round(time.Second); lasts > s.options.maxLifetime {
			return promptrun.Result{}, fmt.Errorf("substrate run %s may last %s, longer than the backend's maxLifetime of %s, after which its environment may be swept as left behind; shorten its timeout or raise maxLifetime", run, lasts, s.options.maxLifetime)
		}
		lifetime = time.Until(deadline)
	}
	repo, start, err := s.startingPoint(ctx, firstNonEmpty(in.Repo, spec.Cwd(), "."), run)
	if err != nil {
		return promptrun.Result{}, err
	}
	workspace, err := bundleWorkspace(ctx, repo, run, start, setupNames(spec))
	if err != nil {
		return promptrun.Result{}, err
	}
	defer func() { _ = os.Remove(workspace) }()
	job := remoterun.JobFor(in, in.HostData)
	job.Root = repo
	// The environment is the run's boundary, so the run in it is unsandboxed.
	job.Resolved.Spec.Sandbox = &api.SandboxRef{Mode: api.SandboxOff}
	task, err := json.Marshal(Task{Job: job, Ref: workspaceRef(run)})
	if err != nil {
		return promptrun.Result{}, fmt.Errorf("encode the task of substrate run %s: %w", run, err)
	}
	output := outputLog{to: in.EnvironmentOutput}
	if output.to == nil {
		if output.path, err = logPath(run); err != nil {
			return promptrun.Result{}, err
		}
	}

	client, err := substrate.Dial(s.options.client)
	if err != nil {
		return promptrun.Result{}, err
	}
	defer func() { _ = client.Close() }()
	if s.options.environment == "" {
		s.sweepNow(ctx, client)
	}
	env, err := client.Create(ctx, substrate.Environment{ID: firstNonEmpty(s.options.environment, environmentPrefix+run), Image: s.options.image, Egress: s.options.egress})
	if err != nil {
		return promptrun.Result{}, err
	}
	defer output.wait()
	defer func() {
		if err := env.Delete(context.WithoutCancel(ctx)); err != nil {
			logger.Warnf("substrate run %s ended, but deleting its environment failed, so it is left behind until it is deleted by hand: %v", run, err)
		}
	}()
	if err := writeFile(ctx, env, TaskFile, bytes.NewReader(task)); err != nil {
		return promptrun.Result{}, err
	}
	if err := writeBundle(ctx, env, workspace); err != nil {
		return promptrun.Result{}, err
	}
	process := substrate.Process{Command: s.options.command, Cwd: RunDir, Env: s.options.env, Stdin: true, Timeout: lifetime}
	proc, err := env.Start(ctx, process)
	if err != nil {
		return promptrun.Result{}, err
	}
	if err := output.copy(ctx, proc); err != nil {
		return promptrun.Result{}, err
	}

	if in.OnTranscript == nil {
		if home, homeErr := os.UserHomeDir(); homeErr == nil {
			in.OnTranscript = keepTranscripts(home, run)
		} else {
			logger.Warnf("substrate run %s's transcripts can't be kept here: %v", run, homeErr)
		}
	}
	result, err := remoterun.Relay(ctx, in, started, proc.Stdout(ctx), proc.Input(ctx))
	if ctx.Err() != nil {
		if killErr := proc.Kill(context.WithoutCancel(ctx)); killErr != nil {
			logger.Warnf("substrate run %s stopped, but killing it failed: %v", run, killErr)
		}
		return result, fmt.Errorf("substrate run %s stopped: %w", run, context.Cause(ctx))
	}
	if errors.Is(err, remoterun.ErrNoResult) {
		code, waitErr := proc.Wait(ctx)
		if waitErr != nil {
			return result, fmt.Errorf("substrate run %s ended without a result, and its exit code is unknown (%v); its output %s", run, waitErr, output.where())
		}
		return result, fmt.Errorf("substrate run %s exited %d without a result; its output %s", run, code, output.where())
	}
	if fetchErr := fetchCommits(ctx, env, repo, run, firstNonEmpty(s.options.branch, branch(run)), start, &result); fetchErr != nil {
		return result, errors.Join(err, fetchErr)
	}
	return result, err
}

// environmentPrefix starts the ID of every environment captain names, which a sweep deletes once
// it is older than the backend's maxLifetime. A caller that names its environments sweeps its own.
const environmentPrefix = "captain-run-"

// sweepInterval is how often runs of one backend sweep what earlier runs left behind.
const sweepInterval = 5 * time.Minute

// lastSweeps is when runs of each backend, by its substrate and atespace, last swept.
var lastSweeps sync.Map

// sweepNow sweeps the backend's environments left behind, unless a run of the backend swept in
// the last sweepInterval. A sweep that fails is a warning: the run goes on.
func (s *substrateSandbox) sweepNow(ctx context.Context, client *substrate.Client) {
	key := s.options.client.EnvAPI + "/" + s.options.client.Atespace
	now := time.Now()
	if last, ok := lastSweeps.Load(key); ok && now.Sub(last.(time.Time)) < sweepInterval {
		return
	}
	lastSweeps.Store(key, now)
	swept, err := client.Sweep(ctx, environmentPrefix, now.Add(-s.options.maxLifetime))
	if len(swept) > 0 {
		logger.Infof("deleted %d substrate environments left behind by runs older than %s: %s", len(swept), s.options.maxLifetime, strings.Join(swept, ", "))
	}
	if err != nil {
		logger.Warnf("sweep the substrate environments left behind: %v", err)
	}
}

// SweepBackend deletes the environments of the backend cfg selects that runs older than its
// maxLifetime left behind, and returns their IDs.
func SweepBackend(ctx context.Context, cfg api.SandboxConfig) ([]string, error) {
	options, err := decodeOptions(cfg)
	if err != nil {
		return nil, err
	}
	client, err := substrate.Dial(options.client)
	if err != nil {
		return nil, err
	}
	defer func() { _ = client.Close() }()
	return client.Sweep(ctx, environmentPrefix, time.Now().Add(-options.maxLifetime))
}

// runID names one run: its environment, branch and log.
func runID() (string, error) {
	id := make([]byte, 6)
	if _, err := rand.Read(id); err != nil {
		return "", fmt.Errorf("name a substrate run: %w", err)
	}
	return hex.EncodeToString(id), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// workspaceRef is the ref the caller's checkout goes out on, outside refs/heads so no branch
// list shows it while it exists.
func workspaceRef(run string) string { return "refs/captain/substrate/" + run }

// branch is where the run's commits come back to.
func branch(run string) string { return "captain/substrate/" + run }

// start is where a run starts: base, the caller's commit, and setup, the commit the run gets,
// which is base with the caller's uncommitted work on top, or base itself.
type start struct{ base, setup string }

// startingPoint is the repository the run's commits come back to and the commit the run starts
// from. A run from the backend's ref starts at that commit of dir's repository, which may be
// bare; any other run starts from dir's checkout as it is, uncommitted work included, which is
// left as it is.
func (s *substrateSandbox) startingPoint(ctx context.Context, dir, run string) (string, start, error) {
	if s.options.ref != "" {
		sha, err := gitOutput(ctx, dir, "rev-parse", "--verify", s.options.ref+"^{commit}")
		if err != nil {
			return "", start{}, fmt.Errorf("substrate run %s starts from %s, which %s doesn't have: %w", run, s.options.ref, dir, err)
		}
		return dir, start{base: sha, setup: sha}, nil
	}
	repo, err := gitOutput(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", start{}, fmt.Errorf("sandbox substrate runs a git checkout, or a commit named by the backend's ref: %w", err)
	}
	snapshot, err := gitagent.TakeSnapshot(ctx, repo, gitagent.SnapshotPolicy{})
	if err != nil {
		return "", start{}, fmt.Errorf("snapshot the checkout for substrate run %s: %w", run, err)
	}
	return repo, start{base: snapshot.Base, setup: snapshot.Commit}, nil
}

// bundleWorkspace bundles the commit the run starts from as workspaceRef(run), and returns the
// bundle's path.
func bundleWorkspace(ctx context.Context, repo, run string, start start, setup []string) (string, error) {
	ref := workspaceRef(run)
	refs, temporary, err := setupRefs(ctx, repo, run, setup)
	if err != nil {
		return "", err
	}
	temporary = append(temporary, ref)
	defer func() {
		for _, ref := range temporary {
			_, _ = gitOutput(context.WithoutCancel(ctx), repo, "update-ref", "-d", ref)
		}
	}()
	if _, err := gitOutput(ctx, repo, "update-ref", ref, start.setup); err != nil {
		return "", err
	}
	bundle, err := os.CreateTemp("", "captain-substrate-"+run+"-*.bundle")
	if err != nil {
		return "", err
	}
	_ = bundle.Close()
	if _, err := gitOutput(ctx, repo, append([]string{"bundle", "create", "-q", bundle.Name(), ref}, refs...)...); err != nil {
		_ = os.Remove(bundle.Name())
		return "", err
	}
	return bundle.Name(), nil
}

// setupNames are what the spec's setup checks out besides the workspace: the branch a worktree
// checks out, and the commit a new worktree branches from.
func setupNames(spec api.Spec) []string {
	if spec.Setup == nil || spec.Setup.Checkout == nil || spec.Setup.Checkout.Worktree == nil {
		return nil
	}
	worktree := spec.Setup.Checkout.Worktree
	var names []string
	if worktree.Branch != "" {
		names = append(names, "refs/heads/"+worktree.Branch)
	}
	if worktree.Base != "" {
		names = append(names, worktree.Base)
	}
	return names
}

// setupRefs are the refs that carry names into the bundle: a ref names itself, and a commit gets a
// temporary ref of its own, which the caller deletes. A name repo doesn't have is left out, for
// the run's setup to report.
func setupRefs(ctx context.Context, repo, run string, names []string) (refs, temporary []string, err error) {
	for i, name := range names {
		if _, err := gitOutput(ctx, repo, "rev-parse", "--verify", "--quiet", name+"^{commit}"); err != nil {
			continue
		}
		if full, err := gitOutput(ctx, repo, "rev-parse", "--symbolic-full-name", name); err == nil && strings.HasPrefix(full, "refs/") {
			refs = append(refs, full)
			continue
		}
		ref := fmt.Sprintf("refs/captain/substrate-setup/%s-%d", run, i)
		if _, err := gitOutput(ctx, repo, "update-ref", ref, name+"^{commit}"); err != nil {
			return nil, temporary, err
		}
		refs, temporary = append(refs, ref), append(temporary, ref)
	}
	return refs, temporary, nil
}

func writeFile(ctx context.Context, env *substrate.Env, name string, r io.Reader) error {
	return env.WriteFile(ctx, path.Join(RunDir, name), 0o644, r)
}

func writeBundle(ctx context.Context, env *substrate.Env, bundle string) error {
	file, err := os.Open(bundle)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	return writeFile(ctx, env, WorkspaceBundle, file)
}

// fetchCommits fetches what the run committed into repo as branch into, and points the result's
// workspace at that branch. A run that committed nothing leaves no branch.
func fetchCommits(ctx context.Context, env *substrate.Env, repo, run, into string, start start, result *promptrun.Result) error {
	file, err := env.ReadFile(ctx, path.Join(RunDir, CommitsBundle))
	if err == nil {
		err = saveBundle(ctx, file, repo, run, into)
	}
	if status.Code(err) == codes.NotFound {
		return nil
	}
	if err != nil {
		return fmt.Errorf("bring back the commits of substrate run %s: %w", run, err)
	}
	head, err := gitOutput(ctx, repo, "rev-parse", "refs/heads/"+into)
	if err != nil {
		return err
	}
	if result.Response == nil {
		result.Response = &api.Response{}
	}
	workspace := api.Workspace{}
	if result.Response.Workspace != nil {
		workspace = *result.Response.Workspace
	}
	worktree := api.WorktreeState{Repo: repo, Branch: into, Base: start.base, Setup: start.setup, Head: head}
	if workspace.Worktree != nil {
		// The run's setup made a worktree in the environment: the branch is where it branched
		// from and what setup put on it, which the run's commits came back with.
		worktree.Base, worktree.Setup = workspace.Worktree.Base, workspace.Worktree.Setup
	}
	workspace.Cwd, workspace.Repo, workspace.Worktree = repo, repo, &worktree
	result.Response.Workspace = &workspace
	return nil
}

// saveBundle fetches the commits bundle file holds into repo as branch into.
func saveBundle(ctx context.Context, file io.ReadCloser, repo, run, into string) error {
	defer func() { _ = file.Close() }()
	local, err := os.CreateTemp("", "captain-substrate-"+run+"-commits-*.bundle")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(local.Name()) }()
	_, err = io.Copy(local, file)
	if closeErr := local.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	_, err = gitOutput(ctx, repo, "fetch", "-q", local.Name(), "HEAD:refs/heads/"+into)
	return err
}

// logPath is where the run's stderr goes: beside the captain config, as git-agent's state is.
func logPath(run string) (string, error) {
	config, err := captainconfig.Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(config), ".captain", "sandbox", "substrate", run, "agent.log"), nil
}

// outputLog copies a run's stderr to where its output goes until the process ends: to, the
// caller's, or else a log file of the run's own at path.
type outputLog struct {
	to   io.Writer
	path string
	done chan struct{}
}

func (o *outputLog) copy(ctx context.Context, proc *substrate.Proc) error {
	to, closeTo := o.to, func() error { return nil }
	if to == nil {
		if err := os.MkdirAll(filepath.Dir(o.path), 0o755); err != nil {
			return err
		}
		file, err := os.Create(o.path)
		if err != nil {
			return err
		}
		to, closeTo = file, file.Close
	}
	o.done = make(chan struct{})
	stderr := proc.Stderr(context.WithoutCancel(ctx))
	go func() {
		defer close(o.done)
		defer func() { _ = closeTo() }()
		defer func() { _ = stderr.Close() }()
		_, _ = io.Copy(to, stderr)
	}()
	return nil
}

// where says where the run's output went.
func (o *outputLog) where() string {
	if o.to != nil {
		return "went to the run's own log"
	}
	return "is in " + o.path
}

// wait waits for the copy to finish, which it does once the process is gone.
func (o *outputLog) wait() {
	if o.done != nil {
		<-o.done
	}
}

// gitOutput runs git in dir with the caller's git environment scrubbed, as git-agent runs it.
func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = gitagent.ScrubGitEnv(os.Environ())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}
