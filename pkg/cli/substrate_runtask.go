// `captain sandbox substrate run-task`: the environment's side of a whole run on substrate. It
// checks the caller's workspace out, runs the job, and sends back its result and commits.
package cli

import (
	"context"
	"errors"
	"io"
	"os"

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

// runSubstrateTask runs the task in dir as captain, which adds nothing of a host's to the run.
func runSubstrateTask(ctx context.Context, dir string, out io.Writer, in io.Reader) error {
	return substraterun.RunTask(ctx, dir, out, in, captainHost)
}

// captainHost refuses a run that carries a host's data: captain has no hooks to rebuild from it.
func captainHost(job remoterun.Job, _ string) (remoterun.Host, error) {
	if len(job.Host) > 0 {
		return remoterun.Host{}, errors.New("this run carries its host's data, which captain's run-task rebuilds no hooks from; set the substrate backend's command to the host's own program")
	}
	return remoterun.Host{}, nil
}
