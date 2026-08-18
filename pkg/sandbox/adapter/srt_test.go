package adapter

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/gitagent"
	sandboxruntime "github.com/flanksource/sandbox-runtime/sandbox"
)

type fakeRuntime struct {
	gotConfig sandboxruntime.Config
	closed    bool
}

func (f *fakeRuntime) Command(ctx context.Context, command string, args ...string) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, "srt-wrapper", append([]string{command}, args...)...)
	cmd.Env = []string{"SRT=1"}
	return cmd, nil
}

func (f *fakeRuntime) Close(context.Context) error { f.closed = true; return nil }

func TestSRTAdapter_WrapAndClose(t *testing.T) {
	original := NewSRTRuntime
	t.Cleanup(func() { NewSRTRuntime = original })
	fake := &fakeRuntime{}
	NewSRTRuntime = func(_ context.Context, cfg sandboxruntime.Config) (Runtime, error) {
		fake.gotConfig = cfg
		return fake, nil
	}

	sandbox, err := api.NewSandbox(api.SandboxConfig{Kind: api.SandboxSRT})
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	if _, err := sandbox.Prepare(context.Background(), specWithCwd(cwd)); err != nil {
		t.Fatal(err)
	}

	wrapper, ok := api.SandboxAs[api.CommandWrapper](sandbox)
	if !ok {
		t.Fatal("srt must provide a CommandWrapper (its descriptor declares it)")
	}
	command, args, env, err := wrapper.Wrap(context.Background(), "claude", []string{"-p"}, []string{"DECLARED=1"})
	if err != nil {
		t.Fatal(err)
	}
	if command != "srt-wrapper" || len(args) != 2 || args[0] != "claude" || args[1] != "-p" {
		t.Fatalf("wrapped argv = %q %v", command, args)
	}
	// Runtime env survives AND the request-declared variables ride along.
	if len(env) != 2 || env[0] != "SRT=1" || env[1] != "DECLARED=1" {
		t.Fatalf("wrapped env = %v, want runtime env plus declared vars", env)
	}
	if got := fake.gotConfig.Filesystem.AllowWrite[0]; got != cwd {
		t.Fatalf("confinement cwd = %q, want the Prepare()d spec's %q", got, cwd)
	}

	if err := sandbox.Close(); err != nil {
		t.Fatal(err)
	}
	if !fake.closed {
		t.Fatal("Close must close the live runtime")
	}
}

func TestSRTConfigFor(t *testing.T) {
	cwd := t.TempDir()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	denyRead := []string{
		filepath.Join(home, ".ssh"), filepath.Join(home, ".aws"), filepath.Join(home, ".azure"),
		filepath.Join(home, ".config", "gcloud"), filepath.Join(home, ".kube"),
		filepath.Join(home, ".netrc"), filepath.Join(home, ".git-credentials"),
		filepath.Join(home, ".config", "gh"), filepath.Join(home, ".docker", "config.json"),
		filepath.Join(home, ".npmrc"), filepath.Join(home, ".pypirc"),
		filepath.Join(home, ".docker", "run", "docker.sock"), "/var/run/docker.sock",
		"/run/docker.sock", "/run/containerd/containerd.sock", "/run/podman/podman.sock",
	}
	tests := []struct {
		command string
		domains []string
		env     []string
		state   []string
	}{
		{"claude", []string{"anthropic.com", "*.anthropic.com", "claude.ai", "*.claude.ai"}, []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CONFIG_DIR"}, []string{filepath.Join(home, ".claude"), filepath.Join(home, ".claude.json")}},
		{"codex", []string{"openai.com", "*.openai.com", "chatgpt.com", "*.chatgpt.com"}, []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "CODEX_HOME"}, []string{filepath.Join(home, ".codex")}},
		{"gemini", []string{"google.com", "*.google.com", "googleapis.com", "*.googleapis.com"}, []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}, []string{filepath.Join(home, ".gemini")}},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			// No tokens acquired: the baseline policy must be unchanged, so a
			// sandbox that authenticates from the host login still works.
			got, err := srtConfigFor(tt.command, cwd, nil)
			if err != nil {
				t.Fatal(err)
			}
			want := sandboxruntime.Config{
				Network: sandboxruntime.NetworkConfig{AllowedDomains: tt.domains, DeniedDomains: []string{}},
				Filesystem: sandboxruntime.FilesystemConfig{
					AllowWrite: append([]string{cwd, "/tmp"}, tt.state...),
					DenyRead:   denyRead,
					DenyWrite:  []string{},
				},
				PassthroughEnv: tt.env,
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("srtConfigFor() = %#v, want %#v", got, want)
			}
		})
	}

	t.Run("unsupported command fails loud", func(t *testing.T) {
		_, err := srtConfigFor("bash", cwd, nil)
		if err == nil || !strings.Contains(err.Error(), "does not support CLI command") {
			t.Fatalf("err = %v", err)
		}
	})

	// The redacted-credential swap: once a login has been acquired, the private
	// directory becomes writable and the host's own credential file is hidden.
	// Both halves matter — hiding without a replacement breaks authentication,
	// and replacing without hiding leaves the refresh token reachable.
	t.Run("an acquired login hides the host credential it replaces", func(t *testing.T) {
		acquired := &sandboxTokens{
			credDir:   "/tmp/captain-creds-fixture",
			providers: map[string]bool{"claude": true},
		}
		got, err := srtConfigFor("claude", cwd, acquired)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(got.Filesystem.AllowWrite, acquired.credDir) {
			t.Errorf("credential directory is not writable: %v", got.Filesystem.AllowWrite)
		}
		hostCredential := filepath.Join(home, ".claude", ".credentials.json")
		if !slices.Contains(got.Filesystem.DenyRead, hostCredential) {
			t.Errorf("host credential %s is still readable: %v", hostCredential, got.Filesystem.DenyRead)
		}
		// Only the credential file, not the whole state directory: the CLI still
		// needs its settings, history and project state.
		if slices.Contains(got.Filesystem.DenyRead, filepath.Join(home, ".claude")) {
			t.Error("the whole ~/.claude directory must not be denied")
		}
	})

	t.Run("another CLI's login does not hide this one's credential", func(t *testing.T) {
		codexOnly := &sandboxTokens{
			credDir:   "/tmp/captain-creds-fixture",
			providers: map[string]bool{"codex": true},
		}
		got, err := srtConfigFor("claude", cwd, codexOnly)
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(got.Filesystem.DenyRead, filepath.Join(home, ".claude", ".credentials.json")) {
			t.Error("claude's host credential was hidden with no claude replacement acquired")
		}
	})
}

func specWithCwd(cwd string) *api.Spec {
	spec := &api.Spec{}
	spec.SetCwd(cwd)
	return spec
}

func hookSandboxConfig(denyRead ...string) api.SandboxConfig {
	options := map[string]any{api.SandboxOptionProfile: api.SandboxProfileHook}
	if len(denyRead) > 0 {
		options[api.SandboxOptionDenyRead] = denyRead
	}
	return api.SandboxConfig{Kind: api.SandboxSRT, Options: options}
}

// The hook profile is the boundary for agent-authored commands (issue #40
// R5.2): writes confined to the prepared workspace plus a private scratch dir,
// network denied entirely, no credential passthrough, and provider state
// hidden on top of the host-credential list.
func TestSRTAdapter_HookProfile(t *testing.T) {
	original := NewSRTRuntime
	t.Cleanup(func() { NewSRTRuntime = original })
	fake := &fakeRuntime{}
	NewSRTRuntime = func(_ context.Context, cfg sandboxruntime.Config) (Runtime, error) {
		fake.gotConfig = cfg
		return fake, nil
	}

	repo := t.TempDir()
	sandbox, err := api.NewSandbox(hookSandboxConfig(repo))
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	if _, err := sandbox.Prepare(context.Background(), specWithCwd(workspace)); err != nil {
		t.Fatal(err)
	}
	wrapper, _ := api.SandboxAs[api.CommandWrapper](sandbox)
	command, args, env, err := wrapper.Wrap(context.Background(), "sh", []string{"-c", "make lint"}, []string{"PATH=/usr/bin"})
	if err != nil {
		t.Fatal(err)
	}
	if command != "srt-wrapper" || len(args) != 3 || args[0] != "sh" || args[1] != "-c" || args[2] != "make lint" {
		t.Fatalf("wrapped argv = %q %v", command, args)
	}

	cfg := fake.gotConfig
	if cfg.Network.AllowedDomains == nil || len(cfg.Network.AllowedDomains) != 0 {
		t.Fatalf("allowed domains = %#v, want non-nil empty (network isolation on, everything denied)", cfg.Network.AllowedDomains)
	}
	if len(cfg.Filesystem.AllowWrite) != 2 || cfg.Filesystem.AllowWrite[0] != workspace {
		t.Fatalf("allowWrite = %v, want exactly [workspace, scratch]", cfg.Filesystem.AllowWrite)
	}
	scratch := cfg.Filesystem.AllowWrite[1]
	if !strings.Contains(scratch, "captain-hook-scratch-") {
		t.Fatalf("second allowWrite = %q, want the run's scratch directory", scratch)
	}
	if info, err := os.Stat(scratch); err != nil || !info.IsDir() {
		t.Fatalf("scratch %q must exist before wrap (missing AllowWrite paths are silently skipped): %v", scratch, err)
	}
	for _, path := range cfg.Filesystem.AllowWrite {
		if path == "/tmp" {
			t.Fatal("hook policy must not allow host /tmp, where other runs' trees live")
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		filepath.Join(home, ".ssh"),
		filepath.Join(home, ".claude"),
		filepath.Join(home, ".claude.json"),
		filepath.Join(home, ".codex"),
		filepath.Join(home, ".gemini"),
		repo,
	} {
		found := false
		for _, got := range cfg.Filesystem.DenyRead {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("denyRead %v missing %q", cfg.Filesystem.DenyRead, want)
		}
	}
	if len(cfg.PassthroughEnv) != 0 {
		t.Fatalf("passthroughEnv = %v, want none for hooks", cfg.PassthroughEnv)
	}

	// The environment is explicit: the runtime's env, the declared env, and
	// TMPDIR/HOME re-pointed at the scratch directory.
	want := []string{"SRT=1", "PATH=/usr/bin", "TMPDIR=" + scratch, "HOME=" + scratch}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("wrapped env = %v, want %v", env, want)
	}

	if err := sandbox.Close(); err != nil {
		t.Fatal(err)
	}
	if !fake.closed {
		t.Fatal("Close must close the live runtime")
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Fatalf("Close must remove the scratch directory, stat err = %v", err)
	}
}

// Without Prepare there is no workspace to confine to, and falling back to the
// process working directory would build the policy for the bare receiving
// repository — the exact directory hooks must never touch.
func TestSRTAdapter_HookProfileFailsClosedWithoutWorkspace(t *testing.T) {
	original := NewSRTRuntime
	t.Cleanup(func() { NewSRTRuntime = original })
	NewSRTRuntime = func(_ context.Context, _ sandboxruntime.Config) (Runtime, error) {
		t.Fatal("no runtime may be constructed without a prepared workspace")
		return nil, nil
	}

	sandbox, err := api.NewSandbox(hookSandboxConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sandbox.Prepare(context.Background(), &api.Spec{}); err == nil {
		t.Fatal("Prepare with no cwd must fail closed for the hook profile")
	}

	wrapper, _ := api.SandboxAs[api.CommandWrapper](sandbox)
	if _, _, _, err := wrapper.Wrap(context.Background(), "sh", []string{"-c", "true"}, nil); err == nil {
		t.Fatal("Wrap before a successful Prepare must fail closed")
	}
}

// The none adapter is registered here but confines nothing; resolving it for
// hooks must fail at resolve time rather than let exec hooks run bare (R5.2).
func TestResolveHookWrap_NoneIsRefused(t *testing.T) {
	if _, err := gitagent.ResolveHookWrap("none", t.TempDir()); err == nil ||
		!strings.Contains(err.Error(), "no command wrapper") {
		t.Fatalf("hookSandbox none must be refused loudly, got err = %v", err)
	}
}

// The regression this guards: ResolveHookWrap used to hand out a wrapper with
// no Prepare call at all, so the SRT policy was computed for the hook
// process's working directory instead of the materialized tree.
func TestResolveHookWrap_SRTConfinesTheMaterializedTree(t *testing.T) {
	original := NewSRTRuntime
	t.Cleanup(func() { NewSRTRuntime = original })
	fake := &fakeRuntime{}
	NewSRTRuntime = func(_ context.Context, cfg sandboxruntime.Config) (Runtime, error) {
		fake.gotConfig = cfg
		return fake, nil
	}

	repo := t.TempDir()
	factory, err := gitagent.ResolveHookWrap("srt", repo)
	if err != nil {
		t.Fatal(err)
	}
	tree := t.TempDir()
	wrap, closeWrap, err := factory(context.Background(), tree)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := wrap(context.Background(), "sh", []string{"-c", "true"}, []string{"PATH=/usr/bin"}); err != nil {
		t.Fatal(err)
	}
	if got := fake.gotConfig.Filesystem.AllowWrite[0]; got != tree {
		t.Fatalf("confinement workspace = %q, want the materialized tree %q", got, tree)
	}
	found := false
	for _, path := range fake.gotConfig.Filesystem.DenyRead {
		if path == repo {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("denyRead %v must hide the receiving repository %q", fake.gotConfig.Filesystem.DenyRead, repo)
	}
	if err := closeWrap(); err != nil {
		t.Fatal(err)
	}
	if !fake.closed {
		t.Fatal("the factory's close must close the live runtime")
	}
}
