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
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	sandboxruntime "github.com/flanksource/sandbox-runtime/sandbox"
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

type commandSandbox interface {
	Command(context.Context, string, ...string) (*exec.Cmd, error)
	Close(context.Context) error
}

var newCommandSandbox = func(ctx context.Context, cfg sandboxruntime.Config) (commandSandbox, error) {
	return sandboxruntime.New(ctx, cfg)
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

func newCLICommand(ctx context.Context, command string, args []string, cwd string, sandboxed bool) (*exec.Cmd, func(), error) {
	if !sandboxed {
		return exec.CommandContext(ctx, command, args...), func() {}, nil
	}
	cfg, err := cliSandboxConfig(command, cwd)
	if err != nil {
		return nil, nil, err
	}
	sb, err := newCommandSandbox(ctx, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize sandbox-runtime: %w", err)
	}
	closeSandbox := func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sb.Close(closeCtx)
	}
	cmd, err := sb.Command(ctx, command, args...)
	if err != nil {
		closeSandbox()
		return nil, nil, fmt.Errorf("wrap %s with sandbox-runtime: %w", command, err)
	}
	return cmd, closeSandbox, nil
}

func cliSandboxConfig(command, cwd string) (sandboxruntime.Config, error) {
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return sandboxruntime.Config{}, fmt.Errorf("resolve sandbox working directory: %w", err)
		}
	}
	absoluteCwd, err := filepath.Abs(cwd)
	if err != nil {
		return sandboxruntime.Config{}, fmt.Errorf("resolve sandbox working directory %q: %w", cwd, err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return sandboxruntime.Config{}, fmt.Errorf("resolve sandbox home directory: %w", err)
	}

	domains := []string{}
	passthroughEnv := []string{}
	statePaths := []string{}
	switch filepath.Base(command) {
	case "claude":
		domains = []string{"anthropic.com", "*.anthropic.com", "claude.ai", "*.claude.ai"}
		passthroughEnv = []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"}
		statePaths = []string{filepath.Join(home, ".claude"), filepath.Join(home, ".claude.json")}
	case "codex":
		domains = []string{"openai.com", "*.openai.com", "chatgpt.com", "*.chatgpt.com"}
		passthroughEnv = []string{"OPENAI_API_KEY"}
		statePaths = []string{filepath.Join(home, ".codex")}
	case "gemini":
		domains = []string{"google.com", "*.google.com", "googleapis.com", "*.googleapis.com"}
		passthroughEnv = []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}
		statePaths = []string{filepath.Join(home, ".gemini")}
	default:
		return sandboxruntime.Config{}, fmt.Errorf("sandbox-runtime does not support CLI command %q", command)
	}

	return sandboxruntime.Config{
		Network: sandboxruntime.NetworkConfig{
			AllowedDomains: domains,
			DeniedDomains:  []string{},
		},
		Filesystem: sandboxruntime.FilesystemConfig{
			AllowWrite: append([]string{absoluteCwd, "/tmp"}, statePaths...),
			DenyRead: []string{
				filepath.Join(home, ".ssh"),
				filepath.Join(home, ".aws"),
				filepath.Join(home, ".azure"),
				filepath.Join(home, ".config", "gcloud"),
				filepath.Join(home, ".kube"),
				filepath.Join(home, ".docker", "run", "docker.sock"),
				"/var/run/docker.sock",
				"/run/docker.sock",
				"/run/containerd/containerd.sock",
				"/run/podman/podman.sock",
			},
			DenyWrite: []string{},
		},
		PassthroughEnv: passthroughEnv,
	}, nil
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
