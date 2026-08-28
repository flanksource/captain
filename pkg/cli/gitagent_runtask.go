package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/aiflags"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/gitagent"
)

// GitAgentRunTaskOptions configures the detached sidecar task runner.
type GitAgentRunTaskOptions struct {
	Repo    string `flag:"repo" help:"The sidecar's bare repository"`
	Task    string `flag:"task" help:"Task id to work on"`
	Config  string `flag:"config" help:"Config file to read; a detached agent cannot rely on $HOME"`
	Backend string `flag:"backend" help:"Sandbox backend in ~/.captain.yaml" default:"git-agent"`
}

// RunGitAgentRunTask performs one task end to end on the agent host: read the
// dispatched prompt, run it in the worktree, then commit and push. Its output
// is the agent log; failures are also relayed as terminal supervisor outcomes.
func RunGitAgentRunTask(ctx context.Context, opts GitAgentRunTaskOptions) (_ any, runErr error) {
	started := time.Now()
	reportFailure := true
	defer func() {
		if runErr != nil {
			log.Errorf("git-agent task %s failed after %s: %v", opts.Task, time.Since(started).Round(time.Millisecond), runErr)
			if !reportFailure {
				return
			}
			runtime, err := hookRuntimeFromConfig(opts.Backend)
			if err != nil {
				log.Errorf("git-agent task %s could not load relay configuration: %v", opts.Task, err)
				return
			}
			reportCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			if err := gitagent.ReportTaskFailure(reportCtx, opts.Repo, runtime.Relay, opts.Task, runErr); err != nil {
				log.Errorf("git-agent task %s could not report its failure to the supervisor: %v", opts.Task, err)
				return
			}
			log.Infof("git-agent task %s reported its terminal failure to the supervisor", opts.Task)
		}
	}()
	if strings.TrimSpace(opts.Config) != "" {
		captainconfig.SetPath(opts.Config)
	}
	worktree, taskFile, err := gitagent.TaskPaths(opts.Repo, opts.Task)
	if err != nil {
		return nil, err
	}
	payload, err := gitagent.LoadTaskPayload(taskFile)
	if err != nil {
		return nil, err
	}
	var callerTools *api.CallerToolEndpoint
	if payload.CallerTools {
		if !api.SupportsCallerTools(api.Backend(payload.Backend)) {
			return nil, fmt.Errorf("remote backend %q does not support delegated caller tools", payload.Backend)
		}
		callerTools, err = gitagent.LoadCallerToolEndpoint(opts.Repo, opts.Task)
		if err != nil {
			return nil, fmt.Errorf("load delegated caller tools: %w", err)
		}
	}
	identity := ai.LogIdentity(api.Backend(payload.Backend), payload.Model, payload.Effort)
	log.Infof("git-agent task %s starting %s in %s", opts.Task, identity, worktree)
	if err := runTaskPrompt(ctx, worktree, payload, callerTools); err != nil {
		return nil, fmt.Errorf("running the dispatched prompt: %w", err)
	}
	log.Infof("git-agent task %s agent finished after %s; preparing submission", opts.Task, time.Since(started).Round(time.Millisecond))
	pushAttempted, err := submitWork(ctx, worktree, opts.Task)
	reportFailure = !pushAttempted
	if err != nil {
		return nil, err
	}
	log.Infof("git-agent task %s accepted after %s", opts.Task, time.Since(started).Round(time.Millisecond))
	return nil, nil
}

// runTaskPrompt executes the dispatched prompt in the worktree. The sandbox is
// pinned to none: this process IS the relocated run, so resolving a relocating
// sandbox here would dispatch the task to another agent, and so on (H15).
func runTaskPrompt(
	ctx context.Context,
	worktree string,
	payload gitagent.TaskPayload,
	callerTools *api.CallerToolEndpoint,
) error {
	providerOpts := AIProviderOptions{
		ModelFlags: aiflags.ModelFlags{Model: payload.Model, Backend: payload.Backend, Effort: string(payload.Effort)},
		Sandbox:    "none",
	}
	cfg, err := providerOpts.ToConfig()
	if err != nil {
		return err
	}
	cfg.CallerTools = callerTools
	var req ai.Request
	req.Prompt.User = payload.Prompt
	req.Prompt.System = payload.System
	req.Model = cfg.Model
	req.Budget.Timeout = payload.Timeout
	req.SetCwd(worktree)
	// Editing is the point: a coding agent that cannot write files produces an
	// empty result and an unexplained silence on the supervisor.
	req.Permissions.Mode = api.PermissionAcceptEdits

	if _, err := executePromptRequestFunc(ctx, req, cfg, renderedTimeout(PromptRenderResult{Input: req, Config: cfg}), true); err != nil {
		return err
	}
	return nil
}

// submitWork performs the agent's half of the protocol: stage everything the
// run produced, commit, and push. A run that changed nothing is reported as
// such rather than pushed as an empty success. The boolean becomes true once
// push starts, because a failed push may already have produced an authoritative
// hook verdict that the runner must not replace with a terminal worker error.
func submitWork(ctx context.Context, worktree, task string) (bool, error) {
	log.Infof("git-agent task %s staging workspace changes", task)
	if err := git(ctx, worktree, "add", "-A"); err != nil {
		return false, err
	}
	staged, err := hasStagedChanges(ctx, worktree)
	if err != nil {
		return false, err
	}
	if !staged {
		return false, fmt.Errorf("the agent produced no changes for task %s; nothing to submit", task)
	}
	log.Infof("git-agent task %s committing workspace changes", task)
	if err := git(ctx, worktree, "commit", "-m", "captain: "+task); err != nil {
		return false, err
	}
	// The push carries the work through both hook tiers and blocks until the
	// verdict, so its output is the agent's most important log line.
	log.Infof("git-agent task %s pushing for sidecar and supervisor verification", task)
	return true, git(ctx, worktree, "push")
}

func git(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// hasStagedChanges reports whether the index differs from HEAD. `diff --cached
// --quiet` exits 1 when it does, which is the answer rather than a failure.
func hasStagedChanges(ctx context.Context, dir string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--cached", "--quiet")
	cmd.Dir = dir
	err := cmd.Run()
	if err == nil {
		return false, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return true, nil
	}
	return false, fmt.Errorf("git diff --cached: %w", err)
}

// DefaultAgentCommand is the coding agent a sidecar launches when the backend
// declares none: Captain drives the supervisor-selected CLI runtime in the
// prepared worktree, then commits and pushes the result. A concrete default
// keeps an unconfigured sidecar from silently leaving dispatched work idle.
func DefaultAgentCommand(captainBin, repo, task, configPath string) string {
	command := fmt.Sprintf("%q sandbox git-agent run-task --repo %q --task %q", captainBin, repo, task)
	if strings.TrimSpace(configPath) != "" {
		command += fmt.Sprintf(" --config %q", configPath)
	}
	return command
}
