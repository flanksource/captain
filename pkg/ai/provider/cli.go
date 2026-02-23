package provider

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
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

// clearNestingEnv removes env vars that CLI tools use to detect nested sessions.
func clearNestingEnv(environ []string) []string {
	nestingVars := map[string]bool{
		"CLAUDECODE":            true,
		"CLAUDE_CODE_ENTRYPOINT": true,
	}
	var filtered []string
	for _, e := range environ {
		key, _, _ := strings.Cut(e, "=")
		if !nestingVars[key] {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

func runCLI(ctx context.Context, command string, stdinData []byte) (stdout []byte, stderr string, err error) {
	cmd := exec.CommandContext(ctx, command)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, "", fmt.Errorf("failed to create stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, "", fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		if IsCommandNotFound(err) {
			return nil, "", fmt.Errorf("%w: %v", ai.ErrCLINotFound, err)
		}
		return nil, "", fmt.Errorf("failed to start %s: %w", command, err)
	}

	if _, err := stdin.Write(stdinData); err != nil {
		return nil, "", fmt.Errorf("failed to write to stdin: %w", err)
	}
	_, _ = stdin.Write([]byte("\n"))
	_ = stdin.Close()

	stdoutCh := make(chan []byte, 1)
	stderrCh := make(chan string, 1)
	errCh := make(chan error, 2)

	go func() {
		data, err := io.ReadAll(stdoutPipe)
		if err != nil {
			errCh <- fmt.Errorf("failed to read stdout: %w", err)
			return
		}
		stdoutCh <- data
	}()

	go func() {
		data, err := io.ReadAll(stderrPipe)
		if err != nil {
			errCh <- fmt.Errorf("failed to read stderr: %w", err)
			return
		}
		stderrCh <- string(data)
	}()

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	var stdoutData []byte
	var stderrData string
	var waitErr error

	for range 3 {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			return nil, "", fmt.Errorf("%w: context cancelled", ai.ErrTimeout)
		case e := <-errCh:
			return nil, "", e
		case data := <-stdoutCh:
			stdoutData = data
		case data := <-stderrCh:
			stderrData = data
		case e := <-waitCh:
			waitErr = e
		}
	}

	if waitErr != nil {
		return nil, stderrData, HandleExitError(GetExitCode(waitErr), ParseStderr(stderrData))
	}

	return stdoutData, stderrData, nil
}

func MapClaudeCodeModel(model string) string {
	model = strings.TrimPrefix(model, "claude-code-")

	switch model {
	case "sonnet":
		return "claude-sonnet-4"
	case "sonnet-4", "sonnet-4.0":
		return "claude-sonnet-4"
	case "sonnet-3.5", "sonnet-3-5":
		return "claude-3-5-sonnet-20241022"
	case "opus":
		return "claude-3-opus-20240229"
	case "haiku":
		return "claude-3-5-haiku-20241022"
	default:
		if strings.HasPrefix(model, "claude-") {
			return model
		}
		// Handle versioned names like "opus-4-6" or "sonnet-4-5" -> "claude-opus-4-6"
		for _, family := range []string{"opus", "sonnet", "haiku"} {
			if strings.HasPrefix(model, family+"-") {
				return "claude-" + model
			}
		}
		return "claude-sonnet-4"
	}
}
