package adapter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/api"
	sandboxruntime "github.com/flanksource/sandbox-runtime/sandbox"
)

// Runtime is the slice of sandbox-runtime the adapter uses, kept as an
// interface so tests can substitute a fake.
type Runtime interface {
	Command(ctx context.Context, command string, args ...string) (*exec.Cmd, error)
	Close(context.Context) error
}

// NewSRTRuntime constructs the live sandbox-runtime confinement. A variable so
// tests can stub the runtime without OS-level sandbox support.
var NewSRTRuntime = func(ctx context.Context, cfg sandboxruntime.Config) (Runtime, error) {
	return sandboxruntime.New(ctx, cfg)
}

// srtSandbox confines the agent CLI with sandbox-runtime (filesystem and
// network policy). The confinement is constructed per wrapped command, because
// its policy depends on which CLI is being run.
type srtSandbox struct {
	cwd string

	mu       sync.Mutex
	runtimes []Runtime
}

// SRT is the SandboxFactory for the sandbox-runtime adapter.
func SRT(api.SandboxConfig) (api.Sandbox, error) { return &srtSandbox{}, nil }

func init() { api.RegisterSandbox(api.SandboxSRT, SRT) }

func (s *srtSandbox) Kind() api.SandboxKind { return api.SandboxSRT }

func (s *srtSandbox) Prepare(_ context.Context, spec *api.Spec) (*api.SandboxSession, error) {
	s.cwd = spec.Cwd()
	return &api.SandboxSession{}, nil
}

func (s *srtSandbox) Wrap(ctx context.Context, command string, args, env []string) (string, []string, []string, error) {
	cfg, err := srtConfigFor(command, s.cwd)
	if err != nil {
		return "", nil, nil, err
	}
	runtime, err := NewSRTRuntime(ctx, cfg)
	if err != nil {
		return "", nil, nil, fmt.Errorf("initialize sandbox-runtime: %w", err)
	}
	s.mu.Lock()
	s.runtimes = append(s.runtimes, runtime)
	s.mu.Unlock()

	cmd, err := runtime.Command(ctx, command, args...)
	if err != nil {
		return "", nil, nil, fmt.Errorf("wrap %s with sandbox-runtime: %w", command, err)
	}
	// When the runtime supplies its own environment, the request-declared
	// variables are appended so they survive; when it supplies none, nil is
	// returned and the exec seam falls back to the full resolved environment,
	// which already contains them.
	wrappedEnv := cmd.Env
	if len(wrappedEnv) > 0 {
		wrappedEnv = append(wrappedEnv, env...)
	}
	return cmd.Args[0], cmd.Args[1:], wrappedEnv, nil
}

func (s *srtSandbox) Close() error {
	s.mu.Lock()
	runtimes := s.runtimes
	s.runtimes = nil
	s.mu.Unlock()
	var errs []error
	for _, runtime := range runtimes {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := runtime.Close(closeCtx); err != nil {
			errs = append(errs, err)
		}
		cancel()
	}
	return errors.Join(errs...)
}

// srtConfigFor builds the per-CLI confinement policy: the provider's API
// domains, its credential env vars, and its state directory — nothing else.
func srtConfigFor(command, cwd string) (sandboxruntime.Config, error) {
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

	var domains, statePaths []string
	switch filepath.Base(command) {
	case "claude":
		domains = []string{"anthropic.com", "*.anthropic.com", "claude.ai", "*.claude.ai"}
		statePaths = []string{filepath.Join(home, ".claude"), filepath.Join(home, ".claude.json")}
	case "codex":
		domains = []string{"openai.com", "*.openai.com", "chatgpt.com", "*.chatgpt.com"}
		statePaths = []string{filepath.Join(home, ".codex")}
	case "gemini":
		domains = []string{"google.com", "*.google.com", "googleapis.com", "*.googleapis.com"}
		statePaths = []string{filepath.Join(home, ".gemini")}
	default:
		return sandboxruntime.Config{}, fmt.Errorf("sandbox-runtime does not support CLI command %q", command)
	}
	passthroughEnv := cliCredentialEnv(command)

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
				filepath.Join(home, ".netrc"),
				filepath.Join(home, ".git-credentials"),
				filepath.Join(home, ".config", "gh"),
				filepath.Join(home, ".docker", "config.json"),
				filepath.Join(home, ".npmrc"),
				filepath.Join(home, ".pypirc"),
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
