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
	"sort"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/observation"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/shell"
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

// startCLIStream launches the CLI through the selected sandbox. It takes the
// request itself because the sandbox seam needs three distinct things from
// it: the working directory, the request-DECLARED variables (Setup.EnvVars —
// what must cross a confinement boundary), and the fully RESOLVED environment
// (Setup.Env — what the child process runs with). Collapsing those early is
// how a container ends up receiving either none of the declared variables or
// the entire host environment.
func startCLIStream(ctx context.Context, command string, args []string, stdinData []byte, req *ai.Request, sandbox *api.SandboxConfig) (*exec.Cmd, io.ReadCloser, *bytes.Buffer, func(), error) {
	cmd, closeSandbox, err := newCLICommand(ctx, command, args, req, sandbox)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			closeSandbox()
		}
	}()
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
// nil means the explicit identity adapter.
func newCLICommand(ctx context.Context, command string, args []string, req *ai.Request, sandbox *api.SandboxConfig) (*exec.Cmd, func(), error) {
	cfg := api.SandboxConfig{Kind: api.SandboxOff}
	if sandbox != nil {
		cfg = *sandbox
	}
	return newSandboxedCommand(ctx, cfg, command, args, req)
}

// newSandboxedCommand constructs the selected sandbox adapter and builds the
// command through its CommandWrapper when it provides one — the single exec
// seam shared by claude-cli, codex-cli and gemini-cli. An adapter without the
// capability falls through to a bare command, which is exactly what the "off"
// adapter is; an adapter whose wrapping fails aborts the run rather than
// falling back to an unconfined process.
//
// The adapter sees the real request via Prepare and only the request-DECLARED
// variables via Wrap. The command's environment layers, in order: the
// wrapper's returned env (authoritative when non-empty — a container must
// supply the docker client's environment wholesale), else the resolved setup
// environment; then the Prepare session's Env on top, and the session's
// WorkDir over the request cwd.
func newSandboxedCommand(ctx context.Context, cfg api.SandboxConfig, command string, args []string, req *ai.Request) (*exec.Cmd, func(), error) {
	sandbox, err := api.NewSandbox(cfg)
	if err != nil {
		return nil, nil, err
	}
	closeSandbox := func() { _ = sandbox.Close() }
	session, err := sandbox.Prepare(ctx, req)
	if err != nil {
		closeSandbox()
		return nil, nil, err
	}

	var resolvedEnv []string
	if req.Setup != nil {
		resolvedEnv = req.Setup.Env
	}
	cmd := exec.CommandContext(ctx, command, args...)
	if wrapper, ok := api.SandboxAs[api.CommandWrapper](sandbox); ok {
		wrappedCommand, wrappedArgs, wrappedEnv, err := wrapper.Wrap(ctx, command, args, declaredSetupEnv(req.Setup))
		if err != nil {
			closeSandbox()
			return nil, nil, err
		}
		cmd = exec.CommandContext(ctx, wrappedCommand, wrappedArgs...)
		if len(wrappedEnv) > 0 {
			cmd.Env = wrappedEnv
		}
	}
	if cmd.Env == nil {
		if merged := commandEnv(resolvedEnv); len(merged) > 0 {
			cmd.Env = merged
		}
	}
	if session != nil && len(session.Env) > 0 {
		if cmd.Env == nil {
			cmd.Env = os.Environ()
		}
		cmd.Env = append(cmd.Env, session.Env...)
	}
	capture := observation.RuntimeCaptureFromContext(ctx)
	if len(capture.Environment) > 0 {
		if cmd.Env == nil {
			cmd.Env = os.Environ()
		}
		names := make([]string, 0, len(capture.Environment))
		for name := range capture.Environment {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			cmd.Env = append(cmd.Env, name+"="+capture.Environment[name])
		}
	}
	cmd.Dir = req.Cwd()
	if session != nil && session.WorkDir != "" {
		cmd.Dir = session.WorkDir
	}
	return cmd, closeSandbox, nil
}

// declaredSetupEnv projects the request-declared variables (Setup.EnvVars)
// onto KEY=VALUE pairs using the values Setup.Resolve produced. Setup.Env
// itself is the FULL resolved environment — host environ included — so it
// must never be handed to an adapter as "the declared set": that turns
// "cross the declared variables into the container" into "cross the entire
// host environment".
func declaredSetupEnv(setup *shell.Setup) []string {
	if setup == nil || len(setup.EnvVars) == 0 {
		return nil
	}
	resolved := map[string]string{}
	for _, item := range setup.Env {
		if key, value, ok := strings.Cut(item, "="); ok {
			resolved[key] = value
		}
	}
	var out []string
	for _, declared := range setup.EnvVars {
		if declared.Name == "" {
			continue
		}
		if value, ok := resolved[declared.Name]; ok {
			out = append(out, declared.Name+"="+value)
		}
	}
	return out
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
