// Sidecar workspace setup (§6.1 steps 4–5, run from post-receive where
// quarantine has ended — R6.4/H2): the agent gets an ordinary clone with a
// branch and upstream so a bare `git commit` + `git push` is sufficient
// (R3.1/H17), task.json lands outside the worktree, and the agent process is
// launched fully detached so the dispatch push returns promptly (R6.3/H12).
package gitagent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// NoAgentCommand opts a sidecar out of launching anything, leaving the
// prepared worktree for a human. It is spelled explicitly so that "no agent
// ran" is always a choice on the record rather than an empty config field.
const NoAgentCommand = "none"

// SetupAgentWorkspace creates the task branch in the sidecar repo and clones
// it (object store shared) into <repo>/captain/tasks/<task>/worktree. A clone
// rather than a linked worktree keeps the agent's unaccepted commits in the
// agent's own object store: a rejected push leaves the sidecar repo clean.
func SetupAgentWorkspace(ctx context.Context, sidecarRepo, task, dispatchCommit string) (string, error) {
	branch, err := AgentBranch(task)
	if err != nil {
		return "", err
	}
	env := ScrubGitEnv(os.Environ())
	if _, err := runGit(ctx, sidecarRepo, env, "update-ref", branch, dispatchCommit); err != nil {
		return "", err
	}
	workdir := filepath.Join(taskStateDir(sidecarRepo, task), "worktree")
	if _, err := os.Stat(workdir); err == nil {
		return workdir, nil // re-dispatch onto an existing workspace is a no-op
	}
	branchName := "captain/" + task
	if _, err := runGit(ctx, filepath.Dir(workdir), env,
		"clone", "--quiet", "--shared", "--branch", branchName, sidecarRepo, workdir); err != nil {
		return "", err
	}
	// Pin the agent's identity so a bare `git commit` needs no global config.
	for _, kv := range [][2]string{{"user.name", "captain-agent"}, {"user.email", "agent@captain.local"}} {
		if _, err := runGit(ctx, workdir, env, "config", kv[0], kv[1]); err != nil {
			return "", err
		}
	}
	return workdir, nil
}

// WriteTaskFile materializes task.json outside the worktree (§4).
func WriteTaskFile(sidecarRepo, task string, payload []byte) (string, error) {
	dir := taskStateDir(sidecarRepo, task)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, ControlTaskFile)
	return path, writeFileAtomic(path, payload, 0o644)
}

// LaunchAgent starts command (a shell line) fully detached: its own session,
// stdio redirected to files. A child inheriting the hook's stdout keeps the
// sideband pipe open and receive-pack waits for EOF — the dispatch push would
// hang for the agent's lifetime (R6.3/H12).
//
// An empty command is an error, not a no-op. Nothing would run, nothing would
// push, and the dispatch would wait out its whole budget for work that was
// never started — a silence indistinguishable from an agent still thinking.
// Callers that genuinely want a bare workspace pass NoAgentCommand.
func LaunchAgent(sidecarRepo, task, workdir, taskFile, command string) error {
	if strings.TrimSpace(command) == "" {
		return fmt.Errorf("task %s: no agent command configured; nothing would run and the dispatch would wait for work that never started", task)
	}
	dir := taskStateDir(sidecarRepo, task)
	if command == NoAgentCommand {
		// Explicitly hand the workspace to a human: record why nothing ran so
		// a waiting dispatch is diagnosable from the task directory.
		return writeFileAtomic(filepath.Join(dir, "agent.stdout.log"),
			[]byte("agentCommand is "+NoAgentCommand+": the worktree is prepared but no agent was launched.\n"+
				"Commit and push from "+workdir+" to complete this task.\n"), 0o644)
	}
	stdout, err := os.OpenFile(filepath.Join(dir, "agent.stdout.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer stdout.Close()
	stderr, err := os.OpenFile(filepath.Join(dir, "agent.stderr.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer stderr.Close()
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = workdir
	cmd.Env = envWith(ScrubGitEnv(os.Environ()),
		"CAPTAIN_TASK="+task,
		"CAPTAIN_TASK_FILE="+taskFile,
	)
	cmd.Stdin = nil
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := markInheritedDescriptorsCloseOnExec(); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launching agent: %w", err)
	}
	// Detach: the hook must not wait. Release drops our handle; the process
	// reparents to init when the hook exits.
	return cmd.Process.Release()
}

// markInheritedDescriptorsCloseOnExec prevents receive-pack's sideband pipes
// from surviving in the detached agent and keeping the dispatch push open.
func markInheritedDescriptorsCloseOnExec() error {
	closeRangeErr := unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC)
	if closeRangeErr == nil {
		return nil
	}
	entries, readErr := os.ReadDir("/proc/self/fd")
	if readErr != nil {
		return fmt.Errorf("marking inherited descriptors close-on-exec: close_range: %v; /proc/self/fd: %w", closeRangeErr, readErr)
	}
	for _, entry := range entries {
		fd, parseErr := strconv.Atoi(entry.Name())
		if parseErr == nil && fd >= 3 {
			syscall.CloseOnExec(fd)
		}
	}
	return nil
}
