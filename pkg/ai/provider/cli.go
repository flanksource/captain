package provider

import (
	"bufio"
	"errors"
	"fmt"
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
		return "claude-sonnet-4"
	}
}
