// Tests RunTask with a host's program: the hooks the host rebuilds from the job's data run in the
// environment's checkout, and what they leave comes back with the run's commits.
package substraterun

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/promptrun"
	"github.com/flanksource/captain/pkg/promptrun/remoterun"
	"github.com/flanksource/commons-db/shell"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// answering answers every turn with text, as a model would.
type answering struct{ text string }

func (p answering) Execute(context.Context, ai.Request) (*ai.Response, error) {
	return &ai.Response{Text: p.text}, nil
}
func (p answering) GetModel() string        { return "claude-sonnet-5" }
func (p answering) GetRuntime() api.Runtime { return api.RuntimeOf(api.Anthropic, api.ModeAgent) }

// writesFile is a host's hook, rebuilt in the environment, that leaves a file in the checkout.
type writesFile struct{ path, content string }

func (h writesFile) Name() string { return "writes-file" }
func (h writesFile) PreRun(*agent.HookContext) error {
	if err := os.MkdirAll(filepath.Dir(h.path), 0o755); err != nil {
		return err
	}
	content := h.content
	if content == "" {
		content = "from the host's hook\n"
	}
	return os.WriteFile(h.path, []byte(content), 0o644)
}

func TestAHostsProgramRunsTheHooksItRebuildsInTheCheckout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	git := func(dir string, args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=caller", "-c", "user.email=caller@example.com"}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
		return string(out)
	}
	git(repo, "init", "-q")
	git(repo, "commit", "-q", "--allow-empty", "-m", "first")
	const ref = "refs/captain/substrate/0123456789ab"
	git(repo, "update-ref", ref, "HEAD")
	dir := t.TempDir()
	git(repo, "bundle", "create", "-q", filepath.Join(dir, WorkspaceBundle), ref)

	spec := api.Spec{Model: api.Model{Name: "claude-sonnet-5", Provider: api.Anthropic, Mode: api.ModeAgent}, Prompt: api.Prompt{User: "fix the build"}}
	spec.SetCwd(repo)
	job := remoterun.JobFor(promptrun.Input{Resolved: api.ResolvedSpec{Spec: spec}, Timeout: time.Minute}, json.RawMessage(`{"file":"hosted.txt"}`))
	job.Root = repo
	task, err := json.Marshal(Task{Job: job, Ref: ref})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, TaskFile), task, 0o644))

	var checkout string
	hostFor := func(job remoterun.Job, src string, call remoterun.HostCall) (remoterun.Host, error) {
		checkout = src
		answer, err := call("greet", json.RawMessage(`"the environment"`))
		if err != nil {
			return remoterun.Host{}, err
		}
		var data struct{ File string }
		if err := json.Unmarshal(job.Host, &data); err != nil {
			return remoterun.Host{}, err
		}
		return remoterun.Host{Provider: answering{text: "fixed"}, Hooks: []any{
			writesFile{path: filepath.Join(src, data.File)},
			writesFile{path: filepath.Join(src, "greeting.json"), content: string(answer)},
			// Stands in for the agent's transcript.
			writesFile{path: filepath.Join(home, ".claude", "projects", "-tmp-captain-run-src", "session-1.jsonl")},
		}}, nil
	}
	stdoutR, stdoutW := io.Pipe()
	stdinR, stdinW := io.Pipe()
	ran := make(chan error, 1)
	// The run's directory is the program's working directory, as substrate starts it.
	t.Chdir(dir)
	go func() {
		ran <- RunTask(t.Context(), ".", stdoutW, stdinR, hostFor)
		_ = stdoutW.Close()
	}()
	transcripts := map[string]string{}
	in := promptrun.Input{OnTranscript: func(source, name string, data []byte) {
		transcripts[source+"/"+name] = string(data)
	}, HostCalls: map[string]func(context.Context, json.RawMessage) (json.RawMessage, error){
		"greet": func(_ context.Context, data json.RawMessage) (json.RawMessage, error) {
			var who string
			if err := json.Unmarshal(data, &who); err != nil {
				return nil, err
			}
			return json.Marshal("hello " + who)
		},
	}}
	result, err := remoterun.Relay(t.Context(), in, func(api.Spec) error { return nil }, stdoutR, stdinW)
	_ = stdinW.Close()
	require.NoError(t, err)
	require.NoError(t, <-ran)

	assert.Equal(t, "fixed", result.Response.Text)
	assert.Equal(t, filepath.Join(dir, "src"), checkout)
	assert.Equal(t, "greeting.json\nhosted.txt\n", git(checkout, "show", "--name-only", "--format=", "HEAD"), "the hooks' files came back in the run's commit")
	assert.Equal(t, `"hello the environment"`, git(checkout, "show", "HEAD:greeting.json"), "the host's program called its caller")
	assert.FileExists(t, filepath.Join(dir, CommitsBundle))
	assert.Equal(t, map[string]string{"claude/-tmp-captain-run-src/session-1.jsonl": "from the host's hook\n"}, transcripts,
		"the agent's transcript came to the caller before the result")
}

func TestARunInASetupWorktreeBringsBackItsBranch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	git := func(dir string, args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=caller", "-c", "user.email=caller@example.com"}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
		return string(out)
	}
	git(repo, "init", "-q")
	git(repo, "commit", "-q", "--allow-empty", "-m", "first")
	const ref = "refs/captain/substrate/0123456789ab"
	git(repo, "update-ref", ref, "HEAD")
	dir := t.TempDir()
	git(repo, "bundle", "create", "-q", filepath.Join(dir, WorkspaceBundle), ref)

	spec := api.Spec{Model: api.Model{Name: "claude-sonnet-5", Provider: api.Anthropic, Mode: api.ModeAgent}, Prompt: api.Prompt{User: "fix the build"}}
	spec.SetCwd(repo)
	spec.Setup.Checkout = &shell.Checkout{Mode: shell.CheckoutLocal, Worktree: &shell.Worktree{Mode: shell.WorktreeNew}}
	// The verify command stands in for the agent's edit, which it leaves uncommitted in the worktree.
	spec.Workflow = &api.Workflow{Verify: &api.Verify{Commands: []string{"echo Paris > answer.txt"}}}
	job := remoterun.JobFor(promptrun.Input{Resolved: api.ResolvedSpec{Spec: spec}, Timeout: time.Minute}, nil)
	job.Root = repo
	task, err := json.Marshal(Task{Job: job, Ref: ref})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, TaskFile), task, 0o644))

	hostFor := func(remoterun.Job, string, remoterun.HostCall) (remoterun.Host, error) {
		return remoterun.Host{Provider: answering{text: "fixed"}}, nil
	}
	stdoutR, stdoutW := io.Pipe()
	stdinR, stdinW := io.Pipe()
	ran := make(chan error, 1)
	go func() {
		ran <- RunTask(t.Context(), dir, stdoutW, stdinR, hostFor)
		_ = stdoutW.Close()
	}()
	result, err := remoterun.Relay(t.Context(), promptrun.Input{}, func(api.Spec) error { return nil }, stdoutR, stdinW)
	_ = stdinW.Close()
	require.NoError(t, err)
	require.NoError(t, <-ran)
	require.NotNil(t, result.Response.Workspace)
	require.NotNil(t, result.Response.Workspace.Worktree, "the run worked in a worktree")

	bundle := filepath.Join(dir, CommitsBundle)
	require.FileExists(t, bundle)
	fetched := t.TempDir()
	git(fetched, "init", "-q")
	git(fetched, "fetch", "-q", filepath.Join(dir, WorkspaceBundle), ref+":"+ref)
	git(fetched, "fetch", "-q", bundle, "HEAD:refs/heads/back")
	assert.Equal(t, "Paris\n", git(fetched, "show", "back:answer.txt"), "the worktree's work came back")
}
