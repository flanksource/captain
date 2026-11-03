// Tests a whole run on substrate against the in-process substrate, whose processes are real: the
// test binary is the environment's program, playing the run's side of the channel.
package substraterun

import (
	"context"
	"encoding/json"
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
	"github.com/flanksource/captain/pkg/substrate/substratetest"
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
	if err := git("", "init", "-q", "src"); err != nil {
		return err
	}
	if err := git("src", "fetch", "-q", "../"+WorkspaceBundle, task.Ref+":"+task.Ref); err != nil {
		return err
	}
	if err := git("src", "switch", "-q", "-c", "run", task.Ref); err != nil {
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
	dirty, err := os.ReadFile("src/dirty.txt")
	if err != nil {
		return fmt.Errorf("the caller's uncommitted work didn't come along: %w", err)
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
	text := fmt.Sprintf("fixed after %s, with %s", reply.Output, strings.TrimSpace(string(dirty)))
	return serve.Send(remoterun.ResultMessage(promptrun.Result{Response: &api.Response{Text: text}, Passed: true}, nil))
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

func TestACallersHooksAreRefused(t *testing.T) {
	f := newFixture(t)
	in := f.input("fix the build")
	in.Hooks = []any{struct{}{}}

	_, err := promptrun.Run(t.Context(), in)
	require.EqualError(t, err, `sandbox substrate runs the whole run in its environment, where the caller's hooks can't run; run them in the environment's own program instead`)
	assert.Empty(t, f.server.Deleted(), "nothing was created")
}
