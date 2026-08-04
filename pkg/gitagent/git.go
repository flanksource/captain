package gitagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// normalizationArgs pin every git invocation this package makes to byte
// fidelity (R6.2): no CRLF conversion and no user/global attribute file, so
// the bytes committed are the bytes on disk regardless of host config.
var normalizationArgs = []string{
	"-c", "core.autocrlf=false",
	"-c", "core.eol=lf",
	"-c", "core.attributesFile=/dev/null",
}

// runGit executes `git <args...>` in dir with the given environment and
// returns trimmed stdout, failing loud with stderr context on any error.
func runGit(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	return runGitIn(ctx, dir, env, nil, args...)
}

// runGitIn is runGit with a stdin stream, for plumbing commands that read
// their payload from stdin (hash-object --stdin, update-index --index-info).
func runGitIn(ctx context.Context, dir string, env []string, stdin io.Reader, args ...string) (string, error) {
	out, err := runGitRaw(ctx, dir, env, stdin, args...)
	return strings.TrimSpace(out), err
}

// runGitRaw returns stdout verbatim — status/ls-files -z records start with
// significant bytes (a leading space is a status code) that trimming would
// corrupt.
func runGitRaw(ctx context.Context, dir string, env []string, stdin io.Reader, args ...string) (string, error) {
	full := append(append([]string{}, normalizationArgs...), args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// gitExitCode runs git and returns its exit code, for commands whose non-zero
// exit is an answer rather than a failure.
func gitExitCode(ctx context.Context, dir string, env []string, args ...string) (int, string, error) {
	var stderr bytes.Buffer
	code, out, err := gitExitCodeStderr(ctx, dir, env, &stderr, args...)
	if err != nil {
		return code, out, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return code, out, nil
}

// gitExitCodeStderr is gitExitCode with stderr streamed live to the given
// writer — the relay uses it to pass the upstream's sideband straight through
// to the blocked pusher (R6.6).
func gitExitCodeStderr(ctx context.Context, dir string, env []string, stderr io.Writer, args ...string) (int, string, error) {
	full := append(append([]string{}, normalizationArgs...), args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = env
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if err == nil {
		return 0, stdout.String(), nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), stdout.String(), nil
	}
	return -1, "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
}
