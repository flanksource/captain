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
	"github.com/flanksource/captain/pkg/api"
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

func startCLIStream(ctx context.Context, command string, args []string, stdinData []byte, cwd string, env []string, sandboxed bool) (*exec.Cmd, io.ReadCloser, *bytes.Buffer, func(), error) {
	cmd, closeSandbox, err := newCLICommand(ctx, command, args, cwd, sandboxed)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			closeSandbox()
		}
	}()
	if cwd != "" {
		cmd.Dir = cwd
	}
	if len(env) > 0 {
		cmd.Env = env
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to create stdin pipe: %w", err)
	}
	stderrBuf := &bytes.Buffer{}
	cmd.Stderr = stderrBuf
	if err := cmd.Start(); err != nil {
		if IsCommandNotFound(err) {
			return nil, nil, nil, nil, fmt.Errorf("%w: %v", ai.ErrCLINotFound, err)
		}
		return nil, nil, nil, nil, fmt.Errorf("failed to start %s: %w", command, err)
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
	closeOnError = false
	return cmd, stdout, stderrBuf, closeSandbox, nil
}

// newCLICommand builds the CLI process through the selected sandbox adapter.
// The boolean is the legacy srt toggle carried by api.Config.Sandbox; it maps
// onto the adapter registry rather than a bespoke code path.
func newCLICommand(ctx context.Context, command string, args []string, cwd string, sandboxed bool) (*exec.Cmd, func(), error) {
	kind := api.SandboxNone
	if sandboxed {
		kind = api.SandboxSRT
	}
	return newSandboxedCommand(ctx, api.SandboxConfig{Kind: kind}, command, args, cwd)
}

// newSandboxedCommand constructs the selected sandbox adapter and builds the
// command through its CommandWrapper when it provides one — the single exec
// seam shared by claude-cli, codex-cli and gemini-cli. An adapter without the
// capability falls through to a bare command, which is exactly what the "none"
// adapter is; an adapter whose wrapping fails aborts the run rather than
// falling back to an unconfined process.
func newSandboxedCommand(ctx context.Context, cfg api.SandboxConfig, command string, args []string, cwd string) (*exec.Cmd, func(), error) {
	sandbox, err := api.NewSandbox(cfg)
	if err != nil {
		return nil, nil, err
	}
	closeSandbox := func() { _ = sandbox.Close() }
	spec := &api.Spec{}
	if cwd != "" {
		spec.SetCwd(cwd)
	}
	if _, err := sandbox.Prepare(ctx, spec); err != nil {
		closeSandbox()
		return nil, nil, err
	}
	if wrapper, ok := api.SandboxAs[api.CommandWrapper](sandbox); ok {
		wrappedCommand, wrappedArgs, wrappedEnv, err := wrapper.Wrap(ctx, command, args, nil)
		if err != nil {
			closeSandbox()
			return nil, nil, err
		}
		cmd := exec.CommandContext(ctx, wrappedCommand, wrappedArgs...)
		if len(wrappedEnv) > 0 {
			cmd.Env = wrappedEnv
		}
		return cmd, closeSandbox, nil
	}
	return exec.CommandContext(ctx, command, args...), closeSandbox, nil
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
