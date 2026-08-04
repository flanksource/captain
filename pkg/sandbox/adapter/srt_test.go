package adapter

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/api"
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
	command, args, env, err := wrapper.Wrap(context.Background(), "claude", []string{"-p"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if command != "srt-wrapper" || len(args) != 2 || args[0] != "claude" || args[1] != "-p" {
		t.Fatalf("wrapped argv = %q %v", command, args)
	}
	if len(env) != 1 || env[0] != "SRT=1" {
		t.Fatalf("wrapped env = %v", env)
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
		filepath.Join(home, ".docker", "run", "docker.sock"), "/var/run/docker.sock",
		"/run/docker.sock", "/run/containerd/containerd.sock", "/run/podman/podman.sock",
	}
	tests := []struct {
		command string
		domains []string
		env     []string
		state   []string
	}{
		{"claude", []string{"anthropic.com", "*.anthropic.com", "claude.ai", "*.claude.ai"}, []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"}, []string{filepath.Join(home, ".claude"), filepath.Join(home, ".claude.json")}},
		{"codex", []string{"openai.com", "*.openai.com", "chatgpt.com", "*.chatgpt.com"}, []string{"OPENAI_API_KEY"}, []string{filepath.Join(home, ".codex")}},
		{"gemini", []string{"google.com", "*.google.com", "googleapis.com", "*.googleapis.com"}, []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}, []string{filepath.Join(home, ".gemini")}},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			got, err := srtConfigFor(tt.command, cwd)
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
		_, err := srtConfigFor("bash", cwd)
		if err == nil || !strings.Contains(err.Error(), "does not support CLI command") {
			t.Fatalf("err = %v", err)
		}
	})
}

func specWithCwd(cwd string) *api.Spec {
	spec := &api.Spec{}
	spec.SetCwd(cwd)
	return spec
}
