// Tests `captain sandbox substrate run-task`, the environment's side of a whole run on substrate:
// it checks the caller's workspace out, runs the job against a mocked model, and sends back its
// result and what it committed.
package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/aimock"
	"github.com/flanksource/captain/pkg/aimock/anthropicmock"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/promptrun"
	"github.com/flanksource/captain/pkg/promptrun/remoterun"
	"github.com/flanksource/captain/pkg/sandbox/substraterun"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const workspaceRef = "refs/captain/substrate/0123456789ab"

// runTaskDir is a run's directory as the substrate sandbox leaves it in an environment: the
// caller's checkout in the workspace bundle, and the task.
func runTaskDir(t *testing.T, spec api.Spec) string {
	return runTaskDirWithHost(t, spec, nil)
}

// runTaskDirWithHost is runTaskDir for a run that carries a host's data.
func runTaskDirWithHost(t *testing.T, spec api.Spec, host json.RawMessage) string {
	repo := t.TempDir()
	git := func(dir string, args ...string) {
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=caller", "-c", "user.email=caller@example.com"}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	git(repo, "init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("the repo\n"), 0o644))
	git(repo, "add", "README.md")
	git(repo, "commit", "-q", "-m", "first")
	git(repo, "update-ref", workspaceRef, "HEAD")
	dir := t.TempDir()
	git(repo, "bundle", "create", "-q", filepath.Join(dir, substraterun.WorkspaceBundle), workspaceRef)

	spec.SetCwd(repo)
	in := promptrun.Input{Resolved: api.ResolvedSpec{Spec: spec}, Timeout: time.Minute}
	job := remoterun.JobFor(in, host)
	job.Root = repo
	task, err := json.Marshal(substraterun.Task{Job: job, Ref: workspaceRef})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, substraterun.TaskFile), task, 0o644))
	return dir
}

// capitalModel is the mocked model the run-task specs share. captain caches its API client per
// key and base URL, and the base URL comes from the environment, so every spec must reach the
// same server.
var capitalModel = sync.OnceValues(func() (*anthropicmock.Server, error) {
	scenario, err := aimock.Parse([]byte(`
name: capital
anthropic:
  - match:
      prompt_contains: "capital of France"
    respond:
      text: "The capital of France is Paris."
      stop_reason: end_turn
      usage: {input: 14, output: 8}
    repeat: -1
`))
	if err != nil {
		return nil, err
	}
	return anthropicmock.Start(anthropicmock.Options{Scenario: scenario})
})

// capitalSpec asks the mocked model a question it answers.
func capitalSpec(t *testing.T) api.Spec {
	server, err := capitalModel()
	require.NoError(t, err)
	for _, variable := range server.Env() {
		name, value, _ := strings.Cut(variable, "=")
		t.Setenv(name, value)
	}
	return api.Spec{
		Model:   api.Model{Name: "claude-sonnet-5", Provider: api.Anthropic, Mode: api.ModeAPI},
		Prompt:  api.Prompt{User: "What is the capital of France?"},
		Sandbox: &api.SandboxRef{Mode: api.SandboxOff},
	}
}

// runTask runs the task in dir as the environment's program does, and relays it as the caller.
func runTask(t *testing.T, dir string) (promptrun.Result, []api.Spec, error, error) {
	stdoutR, stdoutW := io.Pipe()
	stdinR, stdinW := io.Pipe()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	ran := make(chan error, 1)
	go func() {
		ran <- runSubstrateTask(ctx, dir, stdoutW, stdinR)
		_ = stdoutW.Close()
	}()
	var started []api.Spec
	result, err := remoterun.Relay(ctx, promptrun.Input{}, func(spec api.Spec) error {
		started = append(started, spec)
		return nil
	}, stdoutR, stdinW)
	_ = stdinW.Close()
	return result, started, err, <-ran
}

func TestRunTaskRunsTheJobInTheCallersCheckout(t *testing.T) {
	spec := capitalSpec(t)
	// The verify command stands in for an agent's edit, which it leaves uncommitted.
	spec.Workflow = &api.Workflow{Verify: &api.Verify{Commands: []string{"echo Paris > answer.txt"}}}
	dir := runTaskDir(t, spec)

	result, started, err, ranErr := runTask(t, dir)
	require.NoError(t, err)
	require.NoError(t, ranErr)
	assert.Equal(t, "The capital of France is Paris.", result.Response.Text)
	require.Len(t, started, 1)
	assert.Equal(t, filepath.Join(dir, "src"), started[0].Cwd(), "the run starts in the environment's checkout")

	checkout := filepath.Join(dir, "src")
	out, err := exec.Command("git", "-C", checkout, "bundle", "list-heads", filepath.Join(dir, substraterun.CommitsBundle)).CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "HEAD")
	log, err := exec.Command("git", "-C", checkout, "log", "--format=%s", workspaceRef+"..HEAD").CombinedOutput()
	require.NoError(t, err, string(log))
	assert.Equal(t, "captain: the work substrate run 0123456789ab left uncommitted\n", string(log))
	answer, err := exec.Command("git", "-C", checkout, "show", "HEAD:answer.txt").CombinedOutput()
	require.NoError(t, err, string(answer))
	assert.Equal(t, "Paris\n", string(answer))
}

func TestRunTaskBringsNothingBackFromARunThatChangedNothing(t *testing.T) {
	dir := runTaskDir(t, capitalSpec(t))

	result, _, err, ranErr := runTask(t, dir)
	require.NoError(t, err)
	require.NoError(t, ranErr)
	assert.Equal(t, "The capital of France is Paris.", result.Response.Text)
	assert.NoFileExists(t, filepath.Join(dir, substraterun.CommitsBundle))
}

func TestRunTaskSendsTheRunsFailureAndExitsWithIt(t *testing.T) {
	spec := capitalSpec(t)
	spec.Prompt.User = "a question the model has no answer for"
	dir := runTaskDir(t, spec)

	_, _, err, ranErr := runTask(t, dir)
	require.Error(t, err, "the caller gets the run's failure")
	assert.Contains(t, err.Error(), "no scenario rule matched this request")
	require.Error(t, ranErr, "and the program exits with it")
	assert.Equal(t, err.Error(), ranErr.Error())
}

func TestRunTaskRefusesAHostsDataItCannotRebuildHooksFrom(t *testing.T) {
	dir := runTaskDirWithHost(t, capitalSpec(t), json.RawMessage(`{"todo":"todo-1"}`))

	_, _, err, ranErr := runTask(t, dir)
	want := "this run carries its host's data, which captain's run-task rebuilds no hooks from; set the substrate backend's command to the host's own program"
	require.EqualError(t, err, want, "the caller is told why")
	require.EqualError(t, ranErr, want)
	assert.NoDirExists(t, filepath.Join(dir, "src"), "nothing ran")
}
