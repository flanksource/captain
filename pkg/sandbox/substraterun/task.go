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

	"github.com/flanksource/captain/pkg/api"
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
	// Git runs in the checkout, so the run's directory must not be relative to where this runs.
	dir, err := filepath.Abs(dir)
	if err != nil {
		return errors.Join(err, serve.Send(remoterun.ResultMessage(promptrun.Result{}, err)))
	}
	task, src, host, err := prepareTask(ctx, dir, hostFor)
	if err != nil {
		return errors.Join(err, serve.Send(remoterun.ResultMessage(promptrun.Result{}, err)))
	}
	finished, err := executeSendingTranscripts(ctx, task.Job, src, host, serve)
	if err != nil {
		return err
	}
	runErr := finished.Err
	if err := bundleCommits(ctx, dir, src, task.Ref, finished.Result); err != nil {
		runErr = errors.Join(runErr, err)
	}
	if err := serve.Send(remoterun.ResultMessage(finished.Result, runErr)); err != nil {
		return errors.Join(runErr, fmt.Errorf("send the run's result to its caller: %w", err))
	}
	return runErr
}

// executeSendingTranscripts executes job, sending the transcripts its agent writes as they change
// and once more after it ends. A transcript that can't be sent is written to stderr, the
// environment's log: the run stands without it.
func executeSendingTranscripts(ctx context.Context, job remoterun.Job, src string, host remoterun.Host, serve remoterun.Serve) (remoterun.Finished, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "the run's transcripts can't be sent: %v\n", err)
		return remoterun.Execute(ctx, job, src, host, serve)
	}
	sender := &transcriptSender{home: home, send: serve.Send}
	sending, stop := context.WithCancel(ctx)
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		sender.every(sending, transcriptInterval, os.Stderr)
	}()
	finished, err := remoterun.Execute(ctx, job, src, host, serve)
	stop()
	<-sent
	if syncErr := sender.sync(); syncErr != nil {
		fmt.Fprintln(os.Stderr, syncErr)
	}
	return finished, err
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

// checkOutWorkspace checks ref out of the workspace bundle into src, on a branch of its own. It
// keeps every ref the bundle carries: ref, which the run's commits are bundled on top of, and the
// branches and commits the run's setup names.
func checkOutWorkspace(ctx context.Context, dir, src, ref string) error {
	if _, err := gitOutput(ctx, dir, "init", "-q", src); err != nil {
		return err
	}
	if _, err := gitOutput(ctx, src, "fetch", "-q", "--update-head-ok", filepath.Join(dir, WorkspaceBundle), "refs/*:refs/*"); err != nil {
		return err
	}
	_, err := gitOutput(ctx, src, "switch", "-q", "-c", "substrate-run", ref)
	return err
}

// bundleCommits commits what the run left uncommitted, and bundles what it committed on top of ref.
// The run's work is in src, or in the worktree its setup made, whose branch src then checks out.
// A run that committed nothing leaves no bundle.
func bundleCommits(ctx context.Context, dir, src, ref string, result promptrun.Result) error {
	run := strings.TrimPrefix(ref, "refs/captain/substrate/")
	worktree := setupWorktree(result)
	if worktree == nil {
		if err := commitLeftovers(ctx, src, run); err != nil {
			return err
		}
	} else {
		if _, err := os.Stat(worktree.Path); err == nil && !worktree.Removed {
			if err := commitLeftovers(ctx, worktree.Path, run); err != nil {
				return err
			}
		}
		if worktree.Branch == "" || worktree.BranchDeleted {
			return nil
		}
		if _, err := gitOutput(ctx, src, "switch", "-q", "--detach", worktree.Branch); err != nil {
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

// setupWorktree is the worktree the run's setup made, when it made one.
func setupWorktree(result promptrun.Result) *api.WorktreeState {
	if result.Response == nil || result.Response.Workspace == nil {
		return nil
	}
	return result.Response.Workspace.Worktree
}

// commitLeftovers commits what run left uncommitted in tree.
func commitLeftovers(ctx context.Context, tree, run string) error {
	if _, err := gitOutput(ctx, tree, "add", "-A"); err != nil {
		return err
	}
	staged, err := gitOutput(ctx, tree, "diff", "--cached", "--name-only")
	if err != nil || staged == "" {
		return err
	}
	_, err = gitOutput(ctx, tree, "-c", "user.name=captain", "-c", "user.email=captain@substrate.invalid", "commit", "-q", "-m",
		"captain: the work substrate run "+run+" left uncommitted")
	return err
}
