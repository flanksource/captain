// Tests a whole run on substrate against the in-process substrate, whose processes are real: the
// test binary is the environment's program, playing the run's side of the channel.
package substraterun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/promptrun"
	"github.com/flanksource/captain/pkg/promptrun/remoterun"
	"github.com/flanksource/captain/pkg/substrate"
	"github.com/flanksource/captain/pkg/substrate/substratetest"
	"github.com/flanksource/commons-db/shell"
	"github.com/flanksource/commons/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// asEnvironment makes the test binary the environment's program.
const asEnvironment = "SUBSTRATERUN_TEST_ENVIRONMENT"

func TestMain(m *testing.M) {
	if os.Getenv(asEnvironment) == "1" {
		if err := environment(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	}
	// A git fetch starts a detached `git maintenance run --auto`, which can still be repacking a
	// test's repository when its temporary directory is removed; the tests' git runs none.
	for key, value := range map[string]string{"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "maintenance.auto", "GIT_CONFIG_VALUE_0": "false"} {
		if err := os.Setenv(key, value); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	os.Exit(m.Run())
}

// environment is the run's side: it checks the workspace out, then does what the prompt says.
func environment() error {
	data, err := os.ReadFile(TaskFile)
	if err != nil {
		return err
	}
	var task Task
	if err := json.Unmarshal(data, &task); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "checking out", task.Ref)
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := checkOutWorkspace(context.Background(), dir, filepath.Join(dir, "src"), task.Ref); err != nil {
		return err
	}
	serve := remoterun.Stdio(os.Stdout, os.Stdin)
	spec := task.Job.Input(filepath.Join(RunDir, "src")).Resolved.Spec
	if spec.Sandbox == nil || spec.Sandbox.Mode != api.SandboxOff || spec.Sandbox.Backend != "" {
		return fmt.Errorf("the environment is the run's boundary, so the run in it is unsandboxed, but its sandbox is %+v", spec.Sandbox)
	}
	switch spec.Prompt.User {
	case "exit without a result":
		fmt.Fprintln(os.Stderr, "the agent crashed")
		os.Exit(3)
	case "hang":
		_ = serve.Send(remoterun.StartedMessage(spec))
		time.Sleep(time.Minute)
	case "echo what the setup names":
		checkout := spec.Setup.Checkout.Worktree
		var found []string
		for _, name := range []string{"refs/heads/" + checkout.Branch, checkout.Base} {
			out, err := exec.Command("git", "-C", "src", "rev-parse", "--verify", name+"^{commit}").Output()
			if err != nil {
				return fmt.Errorf("the environment doesn't have %s, which the run's setup names", name)
			}
			found = append(found, strings.TrimSpace(string(out)))
		}
		return serve.Send(remoterun.ResultMessage(promptrun.Result{Response: &api.Response{Text: strings.Join(found, " ")}, Passed: true}, nil))
	case "echo the run's environment":
		return serve.Send(remoterun.ResultMessage(promptrun.Result{Response: &api.Response{Text: os.Getenv("GAVEL_RUN_ID")}, Passed: true}, nil))
	case "echo the host's data":
		return serve.Send(remoterun.ResultMessage(promptrun.Result{Response: &api.Response{Text: string(task.Job.Host)}, Passed: true}, nil))
	}
	if err := serve.Send(remoterun.StartedMessage(spec)); err != nil {
		return err
	}
	if err := serve.Send(remoterun.EventMessage(1, api.Event{Kind: api.EventText, Text: "fixing"})); err != nil {
		return err
	}
	reply, err := serve.Call(remoterun.ToolCallMessage("todo_search", map[string]any{"query": "build"}))
	if err != nil {
		return err
	}
	dirty := []byte("no work in progress")
	if spec.Prompt.User != "fix the build without the caller's work" {
		if dirty, err = os.ReadFile("src/dirty.txt"); err != nil {
			return fmt.Errorf("the caller's uncommitted work didn't come along: %w", err)
		}
	} else if _, err := os.Stat("src/dirty.txt"); err == nil {
		return errors.New("the caller's uncommitted work came along on a run from a commit")
	}
	if err := os.WriteFile("src/fixed.txt", []byte("fixed\n"), 0o644); err != nil {
		return err
	}
	if err := git("src", "add", "fixed.txt"); err != nil {
		return err
	}
	if err := git("src", "commit", "-q", "-m", "fix the build"); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(CommitsBundle), 0o755); err != nil {
		return err
	}
	if err := git("src", "bundle", "create", "-q", "../"+CommitsBundle, "HEAD", "^"+task.Ref); err != nil {
		return err
	}
	if err := serve.Send(remoterun.TranscriptMessage("claude", "-tmp-captain-run-src/session-1.jsonl", []byte(`{"type":"user"}`+"\n"))); err != nil {
		return err
	}
	text := fmt.Sprintf("fixed after %s, with %s", reply.Output, strings.TrimSpace(string(dirty)))
	response := &api.Response{Text: text}
	if spec.Prompt.User == "fix the build in a setup worktree" {
		start, err := exec.Command("git", "-C", "src", "rev-parse", task.Ref).Output()
		if err != nil {
			return err
		}
		sha := strings.TrimSpace(string(start))
		response.Workspace = &api.Workspace{Worktree: &api.WorktreeState{
			Repo: filepath.Join(RunDir, "src"), Path: filepath.Join(RunDir, "worktree"), Branch: "run", Base: sha, Setup: sha, Head: sha,
		}}
	}
	return serve.Send(remoterun.ResultMessage(promptrun.Result{Response: response, Passed: true}, nil))
}

func git(dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-c", "user.name=agent", "-c", "user.email=agent@example.com"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return nil
}

// fixture is a caller's checkout, with a commit and uncommitted work, and a substrate backend
// whose environment runs this test binary.
type fixture struct {
	server *substratetest.Server
	repo   string
	head   string
	config api.SandboxConfig
}

func newFixture(t *testing.T) fixture {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\n\tname = caller\n\temail = caller@example.com\n"), 0o644))
	repo := t.TempDir()
	run := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("the repo\n"), 0o644))
	run("add", "README.md")
	run("commit", "-q", "-m", "first")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("work in progress\n"), 0o644))

	server := substratetest.Start(t)
	options := server.Options()
	executable, err := os.Executable()
	require.NoError(t, err)
	return fixture{server: server, repo: repo, head: run("rev-parse", "HEAD"), config: api.SandboxConfig{
		Kind: api.SandboxSubstrate, Name: "test-substrate",
		Options: map[string]any{
			"envAPI": options.EnvAPI, "control": options.Control, "image": pinned,
			"command": []any{"env", asEnvironment + "=1", executable},
		},
	}}
}

// captureLog sends the global logger to the returned buffer for the rest of t, so the lines a
// test expects are asserted instead of printed.
func captureLog(t *testing.T) *strings.Builder {
	var buf strings.Builder
	previous := logger.StandardLogger()
	logger.Use(&buf)
	t.Cleanup(func() { logger.SetLogger(previous) })
	return &buf
}

func (f fixture) client(t *testing.T) *substrate.Client {
	client, err := substrate.Dial(f.server.Options())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func (f fixture) git(t *testing.T, args ...string) string {
	out, err := exec.Command("git", append([]string{"-C", f.repo}, args...)...).CombinedOutput()
	require.NoError(t, err, string(out))
	return strings.TrimSpace(string(out))
}

func (f fixture) input(prompt string) promptrun.Input {
	spec := api.Spec{Model: api.Model{Name: "claude-sonnet-5", Provider: api.Anthropic, Mode: api.ModeCLI}, Prompt: api.Prompt{User: prompt}}
	spec.SetCwd(f.repo)
	spec.Sandbox = &api.SandboxRef{Mode: api.SandboxSubstrate, Backend: f.config.Name}
	config := f.config
	return promptrun.Input{
		Resolved: api.ResolvedSpec{Spec: spec},
		Config:   ai.Config{SandboxSelection: &config},
		Timeout:  time.Minute,
	}
}

func TestAWholeRunOnSubstrateComesBackAsABranch(t *testing.T) {
	f := newFixture(t)
	in := f.input("fix the build")
	var events []ai.Event
	in.OnEvent = func(_ int, event ai.Event) { events = append(events, event) }
	in.Config.Tools = []api.ToolDefinition{{Name: "todo_search", DefaultPermission: api.ToolPolicyAllow, Handler: func(_ context.Context, input map[string]any) (any, error) {
		return map[string]any{"found": input["query"]}, nil
	}}}

	result, err := promptrun.Run(t.Context(), in)
	require.NoError(t, err, environmentLog(err))
	assert.Equal(t, `fixed after {"found":"build"}, with work in progress`, result.Response.Text)
	assert.Equal(t, []ai.Event{{Kind: ai.EventText, Text: "fixing"}}, events)

	require.NotNil(t, result.Response.Workspace)
	worktree := result.Response.Workspace.Worktree
	require.NotNil(t, worktree)
	assert.Regexp(t, `^captain/substrate/[0-9a-f]{12}$`, worktree.Branch)
	assert.Equal(t, f.repo, worktree.Repo)
	assert.Equal(t, f.head, worktree.Base)
	assert.Equal(t, f.git(t, "rev-parse", worktree.Branch), worktree.Head)
	assert.Equal(t, "fix the build", f.git(t, "log", "-1", "--format=%s", worktree.Branch), "the agent's commit is the branch's tip")
	assert.Equal(t, worktree.Setup, f.git(t, "rev-parse", worktree.Branch+"~1"), "on the snapshot of the caller's work")
	assert.Equal(t, f.head, f.git(t, "rev-parse", worktree.Setup+"~1"), "which is on the caller's HEAD")
	assert.Equal(t, "fixed", f.git(t, "show", worktree.Branch+":fixed.txt"))
	assert.Equal(t, "work in progress", f.git(t, "show", worktree.Branch+":dirty.txt"), "the run started from the caller's uncommitted work")

	assert.Equal(t, f.head, f.git(t, "rev-parse", "HEAD"), "the caller's checkout is left as it was")
	assert.Equal(t, "?? dirty.txt", f.git(t, "status", "--porcelain"))
	assert.Empty(t, f.git(t, "for-each-ref", "refs/captain"), "the ref the workspace went out on is gone")
	assert.Len(t, f.server.Deleted(), 1, "the environment is deleted")
	transcript, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".claude", "projects", "-tmp-captain-run-src", "session-1.jsonl"))
	require.NoError(t, err, "the run's transcript is kept where the agent would keep it here")
	assert.Equal(t, `{"type":"user"}`+"\n", string(transcript))
}

func TestATranscriptIsKeptOnlyUnderItsAgentsDirectory(t *testing.T) {
	home := "/home/me"
	path, err := transcriptPath(home, "codex", "2026/11/04/rollout-1.jsonl")
	require.NoError(t, err)
	assert.Equal(t, "/home/me/.codex/sessions/2026/11/04/rollout-1.jsonl", path)
	for _, tc := range []struct{ source, name, want string }{
		{"gemini", "a.jsonl", `transcript source "gemini" is not claude or codex`},
		{"claude", "../../.ssh/authorized_keys.jsonl", `transcript name "../../.ssh/authorized_keys.jsonl" must be a relative .jsonl path inside claude's transcripts`},
		{"claude", "/etc/passwd.jsonl", `transcript name "/etc/passwd.jsonl" must be a relative .jsonl path inside claude's transcripts`},
		{"claude", "notes.txt", `transcript name "notes.txt" must be a relative .jsonl path inside claude's transcripts`},
	} {
		_, err := transcriptPath(home, tc.source, tc.name)
		assert.EqualError(t, err, tc.want)
	}
}

// environmentLog is the environment's output that err points at, for a failed run's message.
func environmentLog(err error) string {
	match := regexp.MustCompile(`its output is in (\S+)$`).FindStringSubmatch(fmt.Sprint(err))
	if match == nil {
		return ""
	}
	log, _ := os.ReadFile(match[1])
	return "the environment's output:\n" + string(log)
}

func TestARunThatEndsWithoutAResultSaysWhereItsOutputIs(t *testing.T) {
	f := newFixture(t)

	_, err := promptrun.Run(t.Context(), f.input("exit without a result"))
	require.Error(t, err)
	match := regexp.MustCompile(`^substrate run [0-9a-f]{12} exited 3 without a result; its output is in (\S+)$`).FindStringSubmatch(err.Error())
	require.NotNil(t, match, err.Error())
	log, readErr := os.ReadFile(match[1])
	require.NoError(t, readErr)
	assert.Contains(t, string(log), "the agent crashed")
	assert.Len(t, f.server.Deleted(), 1)
}

func TestAStoppedRunIsKilledAndItsEnvironmentDeleted(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(2*time.Second, cancel)

	begun := time.Now()
	_, err := promptrun.Run(ctx, f.input("hang"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context canceled")
	assert.Less(t, time.Since(begun), 30*time.Second, "the run's process was killed, not waited out")
	assert.Len(t, f.server.Deleted(), 1)
}

func TestACallersHooksWithoutTheirDataAreRefused(t *testing.T) {
	f := newFixture(t)
	in := f.input("fix the build")
	in.Hooks = []any{struct{}{}}

	_, err := promptrun.Run(t.Context(), in)
	require.EqualError(t, err, `sandbox substrate runs the whole run in its environment, where the caller's hooks can't run; set Input.HostData for the environment's program to rebuild them from`)
	assert.Empty(t, f.server.Deleted(), "nothing was created")
}

func TestTheHostsDataReachesTheEnvironmentInPlaceOfItsHooks(t *testing.T) {
	f := newFixture(t)
	in := f.input("echo the host's data")
	in.Hooks = []any{struct{}{}}
	in.HostData = json.RawMessage(`{"todo":"todo-1"}`)

	result, err := promptrun.Run(t.Context(), in)
	require.NoError(t, err, environmentLog(err))
	assert.JSONEq(t, `{"todo":"todo-1"}`, result.Response.Text)
}

func TestARunNamedByItsCallerRunsInTheEnvironmentItNames(t *testing.T) {
	f := newFixture(t)
	in := f.input("fix the build")
	in.Config.SandboxSelection.Options["environment"] = "gavel-run-1"

	_, err := promptrun.Run(t.Context(), in)
	require.NoError(t, err, environmentLog(err))
	assert.Equal(t, []string{"ate-env/gavel-run-1"}, f.server.Deleted())
}

func TestARunFromACommitStartsThereInARepositoryWithoutACheckout(t *testing.T) {
	f := newFixture(t)
	bare := filepath.Join(t.TempDir(), "cache.git")
	out, err := exec.Command("git", "clone", "-q", "--bare", f.repo, bare).CombinedOutput()
	require.NoError(t, err, string(out))
	in := f.input("fix the build without the caller's work")
	in.Repo = bare
	in.Resolved.Spec.SetCwd(bare)
	in.Config.SandboxSelection.Options["ref"] = f.head

	result, err := promptrun.Run(t.Context(), in)
	require.NoError(t, err, environmentLog(err))
	worktree := result.Response.Workspace.Worktree
	require.NotNil(t, worktree)
	assert.Equal(t, bare, worktree.Repo)
	assert.Equal(t, f.head, worktree.Base)
	assert.Equal(t, f.head, worktree.Setup, "nothing of the caller's checkout came along")
	show, err := exec.Command("git", "-C", bare, "show", "--name-only", "--format=", worktree.Branch).CombinedOutput()
	require.NoError(t, err, string(show))
	assert.Equal(t, "fixed.txt\n", string(show), "the run's commit is on the branch, in the bare repository")
}

func TestARunsOutputGoesWhereItsCallerSays(t *testing.T) {
	f := newFixture(t)
	in := f.input("exit without a result")
	var output strings.Builder
	in.EnvironmentOutput = &output

	_, err := promptrun.Run(t.Context(), in)
	require.Error(t, err)
	assert.Regexp(t, `^substrate run [0-9a-f]{12} exited 3 without a result; its output went to the run's own log$`, err.Error())
	assert.Contains(t, output.String(), "the agent crashed")
	assert.NoDirExists(t, filepath.Join(os.Getenv("HOME"), ".captain", "sandbox", "substrate"), "no log file of its own")
}

func TestAResumedSessionIsRefused(t *testing.T) {
	f := newFixture(t)
	in := f.input("fix the build")
	in.Resolved.Spec.SessionID = "session-1"

	_, err := promptrun.Run(t.Context(), in)
	require.EqualError(t, err, `sandbox substrate can't resume session session-1: the run's environment starts without the agent's earlier sessions`)
	assert.Empty(t, f.server.Deleted(), "nothing was created")
}

func TestARunLongerThanTheBackendsLifetimeIsRefused(t *testing.T) {
	f := newFixture(t)
	in := f.input("fix the build")
	in.Config.SandboxSelection.Options["maxLifetime"] = "1m"
	in.Timeout = 2 * time.Minute

	_, err := promptrun.Run(t.Context(), in)
	require.Error(t, err)
	assert.Regexp(t, `^substrate run [0-9a-f]{12} may last 2m0s, longer than the backend's maxLifetime of 1m0s, after which its environment may be swept as left behind; shorten its timeout or raise maxLifetime$`, err.Error())
	assert.Empty(t, f.server.Deleted(), "nothing was created")
}

func TestARunSweepsWhatEarlierRunsLeftBehind(t *testing.T) {
	f := newFixture(t)
	client := f.client(t)
	for _, id := range []string{"captain-run-left", "captain-run-recent", "gavel-run-1"} {
		_, err := client.Create(t.Context(), substrate.Environment{ID: id, Image: pinned})
		require.NoError(t, err)
	}
	f.server.Backdate("ate-env", "captain-run-left", time.Now().Add(-25*time.Hour))
	f.server.Backdate("ate-env", "gavel-run-1", time.Now().Add(-25*time.Hour))
	logged := captureLog(t)

	_, err := promptrun.Run(t.Context(), f.input("fix the build"))
	require.NoError(t, err, environmentLog(err))
	assert.Contains(t, logged.String(), "deleted 1 substrate environments left behind by runs older than 24h0m0s: captain-run-left")
	assert.Contains(t, f.server.Deleted(), "ate-env/captain-run-left", "older than the backend's maxLifetime")
	assert.NotContains(t, f.server.Deleted(), "ate-env/captain-run-recent", "still within its lifetime")
	assert.NotContains(t, f.server.Deleted(), "ate-env/gavel-run-1", "not captain's to sweep")
}

func TestSweepBackendDeletesTheBackendsLeftovers(t *testing.T) {
	f := newFixture(t)
	_, err := f.client(t).Create(t.Context(), substrate.Environment{ID: "captain-run-left", Image: pinned})
	require.NoError(t, err)
	f.server.Backdate("ate-env", "captain-run-left", time.Now().Add(-25*time.Hour))

	swept, err := SweepBackend(t.Context(), f.config)
	require.NoError(t, err)
	assert.Equal(t, []string{"captain-run-left"}, swept)
}

func TestARunWithASetupCheckoutIsHandedOverForTheEnvironmentToSetUp(t *testing.T) {
	f := newFixture(t)
	in := f.input("fix the build")
	in.Resolved.Spec.Setup.Checkout = &shell.Checkout{Mode: shell.CheckoutLocal, Worktree: &shell.Worktree{Mode: shell.WorktreeNew}}

	_, err := promptrun.Run(t.Context(), in)
	require.NoError(t, err, environmentLog(err))
}

func TestARunsOwnEnvironmentVariablesReachItsProcessOnly(t *testing.T) {
	f := newFixture(t)
	in := f.input("echo the run's environment")
	in.Config.SandboxSelection.Options["env"] = map[string]any{"GAVEL_RUN_ID": "run-1", "GAVEL_RUN_TOKEN": "a-run-token"}

	result, err := promptrun.Run(t.Context(), in)
	require.NoError(t, err, environmentLog(err))
	assert.Equal(t, "run-1", result.Response.Text)
}

func TestARunsSetupWorktreeKeepsWhereItBranchedFrom(t *testing.T) {
	f := newFixture(t)

	result, err := promptrun.Run(t.Context(), f.input("fix the build in a setup worktree"))
	require.NoError(t, err, environmentLog(err))
	worktree := result.Response.Workspace.Worktree
	require.NotNil(t, worktree)
	assert.Regexp(t, `^captain/substrate/[0-9a-f]{12}$`, worktree.Branch, "the branch is the one here")
	assert.Equal(t, f.repo, worktree.Repo)
	assert.Empty(t, worktree.Path, "the worktree was in the environment")
	assert.Equal(t, f.git(t, "rev-parse", worktree.Branch), worktree.Head)
	assert.Equal(t, f.git(t, "rev-parse", worktree.Branch+"~1"), worktree.Base, "where the setup worktree branched from, as the environment said")
	assert.Equal(t, worktree.Base, worktree.Setup)
}

func TestTheCommitsARunsSetupNamesGoWithTheWorkspace(t *testing.T) {
	f := newFixture(t)
	f.git(t, "branch", "feature")
	f.git(t, "commit", "-q", "--allow-empty", "-m", "not on any branch")
	loose := f.git(t, "rev-parse", "HEAD")
	f.git(t, "reset", "-q", "--hard", f.head)
	in := f.input("echo what the setup names")
	in.Resolved.Spec.Setup.Checkout = &shell.Checkout{Mode: shell.CheckoutLocal, Worktree: &shell.Worktree{Mode: shell.WorktreeBranch, Branch: "feature", Base: loose}}

	result, err := promptrun.Run(t.Context(), in)
	require.NoError(t, err, environmentLog(err))
	assert.Equal(t, f.head+" "+loose, result.Response.Text)
	assert.Empty(t, f.git(t, "for-each-ref", "refs/captain"), "the refs the commits went out on are gone")
}

func TestARunsCommitsComeBackOnTheBranchItsCallerNames(t *testing.T) {
	f := newFixture(t)
	in := f.input("fix the build")
	in.Config.SandboxSelection.Options["branch"] = "gavel/todo1234-run56789"

	result, err := promptrun.Run(t.Context(), in)
	require.NoError(t, err, environmentLog(err))
	assert.Equal(t, "gavel/todo1234-run56789", result.Response.Workspace.Worktree.Branch)
	assert.Equal(t, "fix the build", f.git(t, "log", "-1", "--format=%s", "gavel/todo1234-run56789"))
}
