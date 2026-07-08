package provider

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
)

func IsCommandNotFound(err error) bool {
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return execErr.Err == exec.ErrNotFound
	}
	return false
}

func GetExitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func ParseStderr(stderr string) string {
	if stderr == "" {
		return ""
	}

	scanner := bufio.NewScanner(strings.NewReader(stderr))
	var errLines []string

	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "error") || strings.Contains(line, "Error") ||
			strings.Contains(line, "failed") || strings.Contains(line, "Failed") {
			errLines = append(errLines, line)
		}
	}

	if len(errLines) > 0 {
		return strings.Join(errLines, "; ")
	}

	lines := strings.Split(stderr, "\n")
	if len(lines) > 5 {
		lines = lines[:5]
	}
	return strings.Join(lines, "; ")
}

func HandleExitError(exitCode int, stderr string) error {
	msg := fmt.Sprintf("CLI exited with code %d", exitCode)
	if stderr != "" {
		msg += fmt.Sprintf(": %s", stderr)
	}

	switch exitCode {
	case 2:
		return fmt.Errorf("invalid arguments: %s", msg)
	case 3:
		return fmt.Errorf("authentication failed: %s", msg)
	case 124:
		return fmt.Errorf("%w: %s", ai.ErrTimeout, msg)
	default:
		return fmt.Errorf("%w: %s", ai.ErrCLIExecutionFailed, msg)
	}
}

func runCLI(ctx context.Context, command string, stdinData []byte, cwd string, env ...[]string) (stdout []byte, stderr string, err error) {
	return runCLICommand(ctx, command, nil, stdinData, cwd, env...)
}

func runCLICommand(ctx context.Context, command string, args []string, stdinData []byte, cwd string, env ...[]string) (stdout []byte, stderr string, err error) {
	cmd := exec.CommandContext(ctx, command, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	if len(env) > 0 && len(env[0]) > 0 {
		cmd.Env = env[0]
	}

	// Buffer stdout/stderr instead of using StdoutPipe/StderrPipe: cmd.Wait closes
	// those pipes when the process exits, so reading them in a goroutine that races
	// Wait fails with "file already closed" (deterministic on Linux). With buffers,
	// Wait blocks until the os/exec output copiers finish, making the reads safe.
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, "", fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		if IsCommandNotFound(err) {
			return nil, "", fmt.Errorf("%w: %v", ai.ErrCLINotFound, err)
		}
		return nil, "", fmt.Errorf("failed to start %s: %w", command, err)
	}

	// Feed stdin in the background so a large prompt cannot deadlock against a
	// process that only drains stdin after producing its output.
	go func() {
		_, _ = stdin.Write(stdinData)
		_, _ = stdin.Write([]byte("\n"))
		_ = stdin.Close()
	}()

	// CommandContext kills the process when ctx is cancelled, which unblocks Wait.
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return nil, "", fmt.Errorf("%w: context cancelled", ai.ErrTimeout)
	}

	stderrData := stderrBuf.String()
	if waitErr != nil {
		return nil, stderrData, HandleExitError(GetExitCode(waitErr), ParseStderr(stderrData))
	}

	return stdoutBuf.Bytes(), stderrData, nil
}

func startCLIStream(ctx context.Context, command string, args []string, stdinData []byte, cwd string, env []string) (*exec.Cmd, io.ReadCloser, *bytes.Buffer, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	if len(env) > 0 {
		cmd.Env = env
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create stdin pipe: %w", err)
	}
	stderrBuf := &bytes.Buffer{}
	cmd.Stderr = stderrBuf
	if err := cmd.Start(); err != nil {
		if IsCommandNotFound(err) {
			return nil, nil, nil, fmt.Errorf("%w: %v", ai.ErrCLINotFound, err)
		}
		return nil, nil, nil, fmt.Errorf("failed to start %s: %w", command, err)
	}
	go func() {
		if len(stdinData) > 0 {
			_, _ = stdin.Write(stdinData)
			if stdinData[len(stdinData)-1] != '\n' {
				_, _ = stdin.Write([]byte("\n"))
			}
		}
		_ = stdin.Close()
	}()
	return cmd, stdout, stderrBuf, nil
}

func finishCLIStream(ctx context.Context, cmd *exec.Cmd, stderrBuf *bytes.Buffer) error {
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return fmt.Errorf("%w: context cancelled", ai.ErrTimeout)
	}
	if waitErr == nil {
		return nil
	}
	return HandleExitError(GetExitCode(waitErr), ParseStderr(stderrBuf.String()))
}

func commandEnv(setupEnv []string) []string {
	if len(setupEnv) == 0 {
		return nil
	}
	merged := os.Environ()
	positions := map[string]int{}
	for i, item := range merged {
		if key, _, ok := strings.Cut(item, "="); ok {
			positions[key] = i
		}
	}
	for _, item := range setupEnv {
		key, _, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			continue
		}
		if idx, ok := positions[key]; ok {
			merged[idx] = item
			continue
		}
		positions[key] = len(merged)
		merged = append(merged, item)
	}
	return merged
}

func emit(ctx context.Context, events chan<- ai.Event, ev ai.Event) bool {
	select {
	case events <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}
