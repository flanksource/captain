// The environment's side of a whole run on substrate, for the program the substrate sandbox
// starts there: it checks the caller's workspace out, runs the job, and sends back the run.
package substraterun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/promptrun"
	"github.com/flanksource/captain/pkg/promptrun/remoterun"
)

// HostFor is what a host's program adds to a run, built from the run's job with its checkout at
// src: the hooks it rebuilds from the job's Host data, and the tools that run beside them.
type HostFor func(job remoterun.Job, src string) (remoterun.Host, error)

// RunTask runs the task in dir, the run's directory, with out and in as its channel to the
// caller. It checks the task's workspace out in dir/src, runs the job there with what hostFor
// adds, commits what the run left uncommitted, bundles what the run committed into
// CommitsBundle, and sends the run's result last. A run that can't start sends why as its result.
// RunTask returns the run's error, after sending it.
func RunTask(ctx context.Context, dir string, out io.Writer, in io.Reader, hostFor HostFor) error {
	serve := remoterun.Stdio(out, in)
	task, src, host, err := prepareTask(ctx, dir, hostFor)
	if err != nil {
		return errors.Join(err, serve.Send(remoterun.ResultMessage(promptrun.Result{}, err)))
	}
	finished, err := remoterun.Execute(ctx, task.Job, src, host, serve)
	if err != nil {
		return err
	}
	runErr := finished.Err
	if err := bundleCommits(ctx, dir, src, task.Ref); err != nil {
		runErr = errors.Join(runErr, err)
	}
	if err := serve.Send(remoterun.ResultMessage(finished.Result, runErr)); err != nil {
		return errors.Join(runErr, fmt.Errorf("send the run's result to its caller: %w", err))
	}
	return runErr
}

// prepareTask reads dir's task, checks its workspace out, and builds what the host adds to it.
func prepareTask(ctx context.Context, dir string, hostFor HostFor) (Task, string, remoterun.Host, error) {
	var task Task
	data, err := os.ReadFile(filepath.Join(dir, TaskFile))
	if err != nil {
		return task, "", remoterun.Host{}, fmt.Errorf("read the substrate run's task: %w", err)
	}
	if err := json.Unmarshal(data, &task); err != nil {
		return task, "", remoterun.Host{}, fmt.Errorf("decode the substrate run's task: %w", err)
	}
	src := filepath.Join(dir, "src")
	host, err := hostFor(task.Job, src)
	if err != nil {
		return task, "", remoterun.Host{}, err
	}
	return task, src, host, checkOutWorkspace(ctx, dir, src, task.Ref)
}

// checkOutWorkspace checks ref out of the workspace bundle into src, on a branch of its own, and
// keeps ref, which the run's commits are bundled on top of.
func checkOutWorkspace(ctx context.Context, dir, src, ref string) error {
	if _, err := gitOutput(ctx, dir, "init", "-q", src); err != nil {
		return err
	}
	if _, err := gitOutput(ctx, src, "fetch", "-q", filepath.Join(dir, WorkspaceBundle), ref+":"+ref); err != nil {
		return err
	}
	_, err := gitOutput(ctx, src, "switch", "-q", "-c", "substrate-run", ref)
	return err
}

// bundleCommits commits what the run left in src, and bundles what it committed on top of ref.
// A run that committed nothing leaves no bundle.
func bundleCommits(ctx context.Context, dir, src, ref string) error {
	if _, err := gitOutput(ctx, src, "add", "-A"); err != nil {
		return err
	}
	staged, err := gitOutput(ctx, src, "diff", "--cached", "--name-only")
	if err != nil {
		return err
	}
	if staged != "" {
		message := "captain: the work substrate run " + strings.TrimPrefix(ref, "refs/captain/substrate/") + " left uncommitted"
		if _, err := gitOutput(ctx, src, "-c", "user.name=captain", "-c", "user.email=captain@substrate.invalid", "commit", "-q", "-m", message); err != nil {
			return err
		}
	}
	committed, err := gitOutput(ctx, src, "rev-list", "--count", ref+"..HEAD")
	if err != nil {
		return err
	}
	if committed == "0" {
		return nil
	}
	bundle := filepath.Join(dir, CommitsBundle)
	if err := os.MkdirAll(filepath.Dir(bundle), 0o755); err != nil {
		return err
	}
	_, err = gitOutput(ctx, src, "bundle", "create", "-q", bundle, "HEAD", "^"+ref)
	return err
}
