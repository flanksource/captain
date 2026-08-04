package adapter

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/api"
)

func prepareContainer(t *testing.T, cwd string, options map[string]any) api.Sandbox {
	t.Helper()
	sandbox, err := api.NewSandbox(api.SandboxConfig{Kind: api.SandboxContainer, Options: options})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sandbox.Close() })
	if _, err := sandbox.Prepare(context.Background(), specWithCwd(cwd)); err != nil {
		t.Fatal(err)
	}
	return sandbox
}

func wrap(t *testing.T, sandbox api.Sandbox, command string, args []string) (string, []string) {
	t.Helper()
	wrapper, ok := api.SandboxAs[api.CommandWrapper](sandbox)
	if !ok {
		t.Fatal("container must provide a CommandWrapper (its descriptor declares it)")
	}
	cmd, wrappedArgs, _, err := wrapper.Wrap(context.Background(), command, args, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cmd, wrappedArgs
}

func TestContainerAdapter_WrapsWithDockerRun(t *testing.T) {
	cwd := t.TempDir()
	sandbox := prepareContainer(t, cwd, map[string]any{"image": "claude-env:golang"})

	command, args := wrap(t, sandbox, "claude", []string{"-p", "hi"})

	if command != "docker" {
		t.Fatalf("command = %q", command)
	}
	argv := strings.Join(args, " ")
	for _, want := range []string{
		"run --rm -i",
		"-w " + cwd,
		"-v " + cwd + ":" + cwd,
		"claude-env:golang claude -p hi",
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv %q missing %q", argv, want)
		}
	}
}

func TestContainerAdapter_PassesCredentialEnvByName(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	sandbox := prepareContainer(t, t.TempDir(), map[string]any{"image": "img"})

	_, args := wrap(t, sandbox, "claude", nil)

	if i := slices.Index(args, "ANTHROPIC_API_KEY"); i < 1 || args[i-1] != "-e" {
		t.Fatalf("argv %v must pass ANTHROPIC_API_KEY by name via -e", args)
	}
	if strings.Contains(strings.Join(args, " "), "sk-test") {
		t.Fatal("credential value must never appear in the argv")
	}
}

func TestContainerAdapter_LoadsProjectConfig(t *testing.T) {
	cwd := t.TempDir()
	config := `
image: claude-env:custom
envPassthrough: [MY_TOKEN]
volumes:
  - source: /data
    target: /data
    readOnly: true
`
	if err := os.WriteFile(filepath.Join(cwd, ".container-sandbox.yaml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MY_TOKEN", "value")
	sandbox := prepareContainer(t, cwd, nil)

	_, args := wrap(t, sandbox, "claude", nil)

	argv := strings.Join(args, " ")
	for _, want := range []string{"claude-env:custom", "-e MY_TOKEN", "-v /data:/data:ro"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv %q missing %q", argv, want)
		}
	}
}

func TestContainerAdapter_NoImageFailsLoud(t *testing.T) {
	sandbox := prepareContainer(t, t.TempDir(), nil)

	wrapper, _ := api.SandboxAs[api.CommandWrapper](sandbox)
	_, _, _, err := wrapper.Wrap(context.Background(), "claude", nil, nil)

	if err == nil || !strings.Contains(err.Error(), "no image") {
		t.Fatalf("err = %v, want a loud no-image failure", err)
	}
}
