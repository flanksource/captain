// `captain sandbox substrate run-task`: the environment's side of a whole run on substrate. It
// checks the caller's workspace out, runs the job, and sends back its result and commits.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/promptrun"
	"github.com/flanksource/captain/pkg/promptrun/remoterun"
	"github.com/flanksource/captain/pkg/sandbox/substraterun"
)

// SubstrateRunTaskOptions configures run-task, which the substrate sandbox starts in the run's
// directory in its environment.
type SubstrateRunTaskOptions struct {
	Dir string `flag:"dir" help:"The run's directory, holding its task and workspace bundle" default:"."`
}

// RunSubstrateRunTask runs the task in the run's directory. Its stdout is the run's channel to
// the caller, so everything else this process prints goes to stderr. It fails when the run did,
// after sending the run's failure to the caller.
func RunSubstrateRunTask(ctx context.Context, opts SubstrateRunTaskOptions) (any, error) {
	channel := os.Stdout
	os.Stdout = os.Stderr
	return nil, runSubstrateTask(ctx, opts.Dir, channel, os.Stdin)
}

// runSubstrateTask checks the task's workspace out in dir/src, runs its job there with out and
// in as the channel to the caller, commits what the run left uncommitted, bundles what the run
// committed into dir/out/commits.bundle, and sends the run's result last. A run that can't start
// sends why as its result.
func runSubstrateTask(ctx context.Context, dir string, out io.Writer, in io.Reader) error {
	serve := remoterun.Stdio(out, in)
	task, src, err := prepareSubstrateTask(ctx, dir)
	if err != nil {
		return errors.Join(err, serve.Send(remoterun.ResultMessage(promptrun.Result{}, err)))
	}
	finished, err := remoterun.Execute(ctx, task.Job, src, remoterun.Host{}, serve)
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

// prepareSubstrateTask reads dir's task and checks its workspace out, returning the checkout.
func prepareSubstrateTask(ctx context.Context, dir string) (substraterun.Task, string, error) {
	var task substraterun.Task
	data, err := os.ReadFile(filepath.Join(dir, substraterun.TaskFile))
	if err != nil {
		return task, "", fmt.Errorf("read the substrate run's task: %w", err)
	}
	if err := json.Unmarshal(data, &task); err != nil {
		return task, "", fmt.Errorf("decode the substrate run's task: %w", err)
	}
	if len(task.Job.Host) > 0 {
		return task, "", errors.New("this run carries its host's data, which captain's run-task rebuilds no hooks from; set the substrate backend's command to the host's own program")
	}
	src := filepath.Join(dir, "src")
	return task, src, checkOutWorkspace(ctx, dir, src, task.Ref)
}

// checkOutWorkspace checks ref out of the workspace bundle into src, on a branch of its own, and
// keeps ref, which the run's commits are bundled on top of.
func checkOutWorkspace(ctx context.Context, dir, src, ref string) error {
	if err := git(ctx, dir, "init", "-q", src); err != nil {
		return err
	}
	if err := git(ctx, src, "fetch", "-q", filepath.Join(dir, substraterun.WorkspaceBundle), ref+":"+ref); err != nil {
		return err
	}
	return git(ctx, src, "switch", "-q", "-c", "substrate-run", ref)
}

// bundleCommits commits what the run left in src, and bundles what it committed on top of ref.
// A run that committed nothing leaves no bundle.
func bundleCommits(ctx context.Context, dir, src, ref string) error {
	if err := git(ctx, src, "add", "-A"); err != nil {
		return err
	}
	staged, err := hasStagedChanges(ctx, src)
	if err != nil {
		return err
	}
	if staged {
		message := "captain: the work substrate run " + strings.TrimPrefix(ref, "refs/captain/substrate/") + " left uncommitted"
		if err := git(ctx, src, "-c", "user.name=captain", "-c", "user.email=captain@substrate.invalid", "commit", "-q", "-m", message); err != nil {
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
	bundle := filepath.Join(dir, substraterun.CommitsBundle)
	if err := os.MkdirAll(filepath.Dir(bundle), 0o755); err != nil {
		return err
	}
	return git(ctx, src, "bundle", "create", "-q", bundle, "HEAD", "^"+ref)
}

// gitOutput is git's output, trimmed; its error carries git's stderr.
func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}
