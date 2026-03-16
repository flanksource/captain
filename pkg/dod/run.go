package dod

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const maxOutputLines = 100

func RunCommands(dod *DodFile) *LastRun {
	run := &LastRun{At: time.Now().UTC()}
	for _, cmd := range dod.Commands {
		result := runCommand(cmd, dod.Workdir, time.Duration(dod.Timeout)*time.Second)
		run.Results = append(run.Results, result)
		if !result.Passed {
			break // fail-fast
		}
	}
	return run
}

func runCommand(command, workdir string, timeout time.Duration) CommandResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = workdir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if ctx.Err() == context.DeadlineExceeded {
			exitCode = -1
		} else {
			exitCode = -1
		}
	}

	return CommandResult{
		Command:  command,
		ExitCode: exitCode,
		Passed:   exitCode == 0,
		Stdout:   truncateOutput(stdout.String()),
		Stderr:   truncateOutput(stderr.String()),
	}
}

func truncateOutput(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= maxOutputLines {
		return s
	}
	return fmt.Sprintf("... (%d lines truncated)\n%s", len(lines)-maxOutputLines, strings.Join(lines[len(lines)-maxOutputLines:], "\n"))
}

func FormatFailureMessage(run *LastRun) string {
	var b strings.Builder
	b.WriteString("DoD checks failed. Fix the issues and try again.\n\n")
	for _, r := range run.Results {
		if r.Passed {
			fmt.Fprintf(&b, "PASS: %s\n", r.Command)
			continue
		}
		fmt.Fprintf(&b, "FAIL: %s (exit code %d)\n", r.Command, r.ExitCode)
		if r.Stdout != "" {
			fmt.Fprintf(&b, "stdout:\n%s\n", r.Stdout)
		}
		if r.Stderr != "" {
			fmt.Fprintf(&b, "stderr:\n%s\n", r.Stderr)
		}
	}
	return b.String()
}
