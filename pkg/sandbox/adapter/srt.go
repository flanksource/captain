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
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/sandbox"
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

// srtSandbox confines a process with sandbox-runtime (filesystem and network
// policy). It carries one of two policies, chosen at construction: the per-CLI
// provider policy (which API domains, credentials and state directory that CLI
// needs), or the generic exec-hook policy (untrusted repository code gets its
// prepared workspace and nothing else). The confinement is constructed per
// wrapped command, because its policy depends on what is being run.
type srtSandbox struct {
	cwd string

	// options are the backend's own settings from ~/.captain.yaml, kept so
	// Prepare can acquire the credentials the `tokens:` block declares.
	options map[string]any
	// tokens are those acquired credentials, nil when none are declared.
	tokens *sandboxTokens

	// hook selects the generic exec-hook profile (api.SandboxProfileHook).
	// Hook policy is a construction-time choice, never inferred from the
	// wrapped argv: keying it off the binary name would hand agent-authored
	// code a provider policy — credentials included — the day a hook invokes
	// a supported CLI.
	hook bool
	// hookDenyRead are extra paths the hook profile must hide (the receiving
	// repository), from api.SandboxOptionDenyRead.
	hookDenyRead []string
	// scratch is the hook run's private writable directory: its TMPDIR and
	// HOME, created by Prepare and removed by Close. It exists so the policy
	// need not allow host /tmp wholesale, where concurrent runs' trees live.
	scratch string

	mu       sync.Mutex
	runtimes []Runtime
}

// SRT is the SandboxFactory for the sandbox-runtime adapter.
func SRT(cfg api.SandboxConfig) (api.Sandbox, error) {
	s := &srtSandbox{options: cfg.Options}
	if profile, _ := cfg.Options[api.SandboxOptionProfile].(string); profile == api.SandboxProfileHook {
		s.hook = true
		s.hookDenyRead = stringSliceOption(cfg.Options, api.SandboxOptionDenyRead)
	}
	return s, nil
}

func init() { api.RegisterSandbox(api.SandboxSRT, SRT) }

func (s *srtSandbox) Kind() api.SandboxKind { return api.SandboxSRT }

func (s *srtSandbox) Prepare(ctx context.Context, spec *api.Spec) (*api.SandboxSession, error) {
	s.cwd = spec.Cwd()
	if s.hook {
		// The hook profile confines to an explicit workspace or not at all: a
		// cwd fallback here would build the policy for whatever directory the
		// receive hook happens to run in — the bare receiving repository.
		if s.cwd == "" {
			return nil, fmt.Errorf("srt hook sandbox requires an explicit workspace; refusing to confine to the process working directory")
		}
		scratch, err := os.MkdirTemp("", "captain-hook-scratch-")
		if err != nil {
			return nil, fmt.Errorf("create hook scratch directory: %w", err)
		}
		s.scratch = scratch
		// The hook profile confines agent-authored repository code, which is
		// exactly what must never hold a credential. Tokens are not acquired
		// for it at all, rather than acquired and then withheld.
		return &api.SandboxSession{}, nil
	}

	tokens, err := acquireSandboxTokens(ctx, s.options)
	if err != nil {
		return nil, err
	}
	s.tokens = tokens
	return &api.SandboxSession{Env: tokens.Env()}, nil
}

func (s *srtSandbox) Wrap(ctx context.Context, command string, args, env []string) (string, []string, []string, error) {
	var cfg sandboxruntime.Config
	var err error
	if s.hook {
		cfg, err = srtHookConfigFor(s.cwd, s.scratch, s.hookDenyRead)
	} else {
		cfg, err = srtConfigFor(command, s.cwd, s.tokens)
	}
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
	// returned and the exec seam falls back to its own environment boundary.
	wrappedEnv := cmd.Env
	if len(wrappedEnv) > 0 {
		wrappedEnv = append(wrappedEnv, env...)
	}
	if s.hook {
		// The hook environment is explicit, never inherited: the caller's env
		// is the whole boundary, and the run's TMPDIR and HOME move into the
		// scratch directory so tools have somewhere writable that is not the
		// tree under verification and not host /tmp.
		if wrappedEnv == nil {
			wrappedEnv = append([]string(nil), env...)
		}
		wrappedEnv = append(wrappedEnv, "TMPDIR="+s.scratch, "HOME="+s.scratch)
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
	if s.scratch != "" {
		if err := os.RemoveAll(s.scratch); err != nil {
			errs = append(errs, err)
		}
		s.scratch = ""
	}
	// The credential directory holds a live access token, so it goes with the
	// sandbox rather than outliving it.
	s.tokens.Cleanup()
	s.tokens = nil
	return errors.Join(errs...)
}

// srtConfigFor builds the per-CLI confinement policy: the provider's API
// domains, its credential env vars, and its state directory — nothing else.
//
// When tokens carry a redacted login for the CLI being wrapped, the policy
// changes shape: the private credential directory becomes writable and the
// host's own credential file becomes unreadable. The swap is conditional
// because it is only safe once a replacement exists — hiding ~/.claude's
// credentials unconditionally would break every sandbox that authenticates
// from the host login today.
func srtConfigFor(command, cwd string, tokens *sandboxTokens) (sandboxruntime.Config, error) {
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
	// tokenProvider names the agent-login token provider that supplies this
	// CLI's credential, empty when the CLI has none.
	var tokenProvider string
	switch filepath.Base(command) {
	case "claude":
		domains = []string{"anthropic.com", "*.anthropic.com", "claude.ai", "*.claude.ai"}
		statePaths = []string{filepath.Join(home, ".claude"), filepath.Join(home, ".claude.json")}
		tokenProvider = "claude"
	case "codex":
		domains = []string{"openai.com", "*.openai.com", "chatgpt.com", "*.chatgpt.com"}
		statePaths = []string{filepath.Join(home, ".codex")}
		tokenProvider = "codex"
	case "gemini":
		domains = []string{"google.com", "*.google.com", "googleapis.com", "*.googleapis.com"}
		statePaths = []string{filepath.Join(home, ".gemini")}
	default:
		return sandboxruntime.Config{}, fmt.Errorf("sandbox-runtime does not support CLI command %q", command)
	}

	allowWrite := append([]string{absoluteCwd, "/tmp"}, statePaths...)
	denyRead := hostCredentialDenyRead(home)
	if dir := tokens.Dir(); dir != "" {
		allowWrite = append(allowWrite, dir)
	}
	if tokenProvider != "" && tokens.Has(tokenProvider) {
		denyRead = append(denyRead, agentLoginDenyRead(home, tokenProvider)...)
	}

	return sandboxruntime.Config{
		Network: sandboxruntime.NetworkConfig{
			AllowedDomains: domains,
			DeniedDomains:  []string{},
		},
		Filesystem: sandboxruntime.FilesystemConfig{
			AllowWrite: allowWrite,
			DenyRead:   denyRead,
			DenyWrite:  []string{},
		},
		PassthroughEnv: cliCredentialEnv(command),
	}, nil
}

// agentLoginDenyRead is the host credential a redacted copy replaces. Only the
// credential file is hidden, not the whole state directory: the CLI still needs
// to read its own settings, history and project state from there.
func agentLoginDenyRead(home, provider string) []string {
	switch provider {
	case "claude":
		return []string{filepath.Join(home, ".claude", ".credentials.json")}
	case "codex":
		return []string{filepath.Join(home, ".codex", "auth.json")}
	}
	return nil
}

// srtHookConfigFor builds the generic exec-hook confinement. The wrapped
// command is agent-authored repository code (issue #40 R5.2), so the policy is
// the inverse of the CLI ones: write access to the materialized workspace and
// the run's scratch directory only (not host /tmp, where other runs' trees
// live), network denied entirely, no credential env passthrough, and reads of
// provider state, captain's own key material and the receiving repository
// hidden on top of the host-credential list. Both directories must already
// exist and be non-empty paths — sandbox-runtime silently skips missing
// AllowWrite entries, which here would mean an unwritable workspace, so this
// fails closed instead.
func srtHookConfigFor(workspace, scratch string, extraDenyRead []string) (sandboxruntime.Config, error) {
	if workspace == "" || scratch == "" {
		return sandboxruntime.Config{}, fmt.Errorf("srt hook sandbox has no prepared workspace; Prepare must run with the materialized tree before Wrap")
	}
	absoluteWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return sandboxruntime.Config{}, fmt.Errorf("resolve hook workspace %q: %w", workspace, err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return sandboxruntime.Config{}, fmt.Errorf("resolve sandbox home directory: %w", err)
	}
	denyRead := append(hostCredentialDenyRead(home),
		// Provider state and credentials the CLI policies deliberately allow.
		filepath.Join(home, ".claude"),
		filepath.Join(home, ".claude.json"),
		filepath.Join(home, ".codex"),
		filepath.Join(home, ".gemini"),
	)
	// Captain's own configuration (which may embed provider keys) and its
	// git-agent key material and served repositories.
	if configPath, err := captainconfig.Path(); err == nil {
		denyRead = append(denyRead, configPath, filepath.Join(filepath.Dir(configPath), ".captain"))
	}
	for _, path := range extraDenyRead {
		if abs, err := filepath.Abs(path); err == nil {
			denyRead = append(denyRead, abs)
		}
	}

	return sandboxruntime.Config{
		Network: sandboxruntime.NetworkConfig{
			// Non-nil and empty: network isolation is ON and every domain is
			// denied. nil would mean "no network restriction" instead.
			AllowedDomains: []string{},
			DeniedDomains:  []string{},
		},
		Filesystem: sandboxruntime.FilesystemConfig{
			AllowWrite: []string{absoluteWorkspace, scratch},
			DenyRead:   denyRead,
			DenyWrite:  []string{},
		},
	}, nil
}

// hostCredentialDenyRead is the host credential material no sandboxed process
// may read, shared by every SRT policy: SSH, cloud, git, container and
// package-manager credentials, plus container runtime sockets.
func hostCredentialDenyRead(home string) []string {
	credentials := []string{
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
	}
	// Shared with git-agent deployment, which refuses to mount the same paths.
	return append(credentials, sandbox.ContainerRuntimeSockets(home)...)
}

func stringSliceOption(options map[string]any, key string) []string {
	switch value := options[key].(type) {
	case []string:
		return value
	case []any:
		var out []string
		for _, item := range value {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
