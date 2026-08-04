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
	sandbox, err := newContainer(t, options)
	if _, err2 := sandbox.Prepare(context.Background(), specWithCwd(cwd)); err2 != nil {
		t.Fatal(err2)
	}
	_ = err
	return sandbox
}

func newContainer(t *testing.T, options map[string]any) (api.Sandbox, error) {
	t.Helper()
	sandbox, err := api.NewSandbox(api.SandboxConfig{Kind: api.SandboxContainer, Options: options})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sandbox.Close() })
	return sandbox, nil
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

// Request-declared setup variables cross by name; values ride the docker
// client environment Wrap returns, never the argv.
func TestContainerAdapter_SetupEnvCrossesByName(t *testing.T) {
	sandbox := prepareContainer(t, t.TempDir(), map[string]any{"image": "img"})
	wrapper, _ := api.SandboxAs[api.CommandWrapper](sandbox)

	_, args, env, err := wrapper.Wrap(context.Background(), "claude", nil,
		[]string{"REVIEW_SENTINEL=present-inside-container", "API_BASE=http://mock:1234"})
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"REVIEW_SENTINEL", "API_BASE"} {
		if i := slices.Index(args, name); i < 1 || args[i-1] != "-e" {
			t.Errorf("argv %v missing name-only -e %s", args, name)
		}
	}
	if strings.Contains(strings.Join(args, " "), "present-inside-container") {
		t.Fatal("setup values must never appear in the docker argv")
	}
	if !slices.Contains(env, "REVIEW_SENTINEL=present-inside-container") {
		t.Fatalf("client env must carry the declared value for docker to resolve; got %d entries", len(env))
	}
}

// The project file is repository content: settings that reach the host are
// refused loudly, naming the trusted place to declare them.
func TestContainerAdapter_UntrustedProjectConfigRejected(t *testing.T) {
	t.Run("envPassthrough refused", func(t *testing.T) {
		cwd := t.TempDir()
		writeProjectConfig(t, cwd, "image: img\nenvPassthrough: [AWS_SECRET_ACCESS_KEY]\n")
		sandbox, _ := newContainer(t, nil)
		_, err := sandbox.Prepare(context.Background(), specWithCwd(cwd))
		if err == nil || !strings.Contains(err.Error(), "~/.captain.yaml") {
			t.Fatalf("err = %v, want refusal naming the trusted config", err)
		}
	})

	t.Run("out-of-project volume refused", func(t *testing.T) {
		cwd := t.TempDir()
		writeProjectConfig(t, cwd, "image: img\nvolumes:\n  - source: /\n    target: /host\n")
		sandbox, _ := newContainer(t, nil)
		_, err := sandbox.Prepare(context.Background(), specWithCwd(cwd))
		if err == nil || !strings.Contains(err.Error(), "outside the project directory") {
			t.Fatalf("err = %v, want out-of-project mount refusal", err)
		}
	})

	t.Run("project-contained volume allowed", func(t *testing.T) {
		cwd := t.TempDir()
		if err := os.MkdirAll(filepath.Join(cwd, "data"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeProjectConfig(t, cwd, "image: img\nvolumes:\n  - source: "+filepath.Join(cwd, "data")+"\n    target: /data\n    readOnly: true\n")
		sandbox := prepareContainer(t, cwd, nil)
		_, args := wrap(t, sandbox, "claude", nil)
		if !strings.Contains(strings.Join(args, " "), "-v "+filepath.Join(cwd, "data")+":/data:ro") {
			t.Fatalf("argv %v missing the project-contained mount", args)
		}
	})
}

// Backend options are the user's own machine config; env, passthrough and
// volumes declared there are honoured — env still by name only in the argv.
func TestContainerAdapter_TrustedBackendOptions(t *testing.T) {
	t.Setenv("MY_TOKEN", "tok")
	sandbox := prepareContainer(t, t.TempDir(), map[string]any{
		"image":          "img",
		"env":            map[string]any{"API_BASE": "http://mock:1234"},
		"envPassthrough": []any{"MY_TOKEN"},
		"volumes":        []any{"/data:/data:ro"},
	})
	wrapper, _ := api.SandboxAs[api.CommandWrapper](sandbox)

	_, args, env, err := wrapper.Wrap(context.Background(), "claude", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	argv := strings.Join(args, " ")
	if i := slices.Index(args, "API_BASE"); i < 1 || args[i-1] != "-e" {
		t.Errorf("argv %v missing name-only -e API_BASE", args)
	}
	if strings.Contains(argv, "http://mock:1234") {
		t.Error("backend env value must not appear in the argv")
	}
	if !slices.Contains(env, "API_BASE=http://mock:1234") {
		t.Error("client env must carry the backend env value")
	}
	if i := slices.Index(args, "MY_TOKEN"); i < 1 || args[i-1] != "-e" {
		t.Errorf("argv %v missing -e MY_TOKEN", args)
	}
	if !strings.Contains(argv, "-v /data:/data:ro") {
		t.Errorf("argv %v missing the trusted volume", args)
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

func writeProjectConfig(t *testing.T, cwd, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(cwd, ".container-sandbox.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
