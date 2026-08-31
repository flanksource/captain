package provider

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/observation"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/sandbox/adapter"
	"github.com/flanksource/commons-db/shell"
	"github.com/flanksource/commons-db/types"
)

// wrapperSandboxStub is a sandbox whose CommandWrapper prefixes the argv, so
// the test can observe the rewrite at the exec seam.
type wrapperSandboxStub struct {
	closed bool
	gotEnv []string
}

func (s *wrapperSandboxStub) Kind() api.SandboxKind { return api.SandboxNone }
func (s *wrapperSandboxStub) Prepare(context.Context, *api.Spec) (*api.SandboxSession, error) {
	return &api.SandboxSession{}, nil
}
func (s *wrapperSandboxStub) Close() error { s.closed = true; return nil }
func (s *wrapperSandboxStub) Wrap(_ context.Context, cmd string, args, env []string) (string, []string, []string, error) {
	s.gotEnv = env
	return "confine", append([]string{"--", cmd}, args...), append(env, "WRAPPED=1"), nil
}

func TestNewCLICommand_UnsandboxedStaysBare(t *testing.T) {
	cmd, cleanup, err := newCLICommand(context.Background(), "echo", []string{"hi"}, requestWithCwd(t.TempDir()), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if len(cmd.Args) != 2 || cmd.Args[0] != "echo" || cmd.Args[1] != "hi" {
		t.Fatalf("args = %v, want the bare argv untouched", cmd.Args)
	}
	if cmd.Env != nil {
		t.Fatalf("env = %v, want inherited (nil)", cmd.Env)
	}
}

func TestNewCLICommandAppliesObservationEnvironmentOnlyFromContext(t *testing.T) {
	ctx := observation.ContextWithRuntimeCapture(context.Background(), observation.RuntimeCaptureConfig{
		Environment: map[string]string{"KUBECONFIG": "/tmp/captain-observe-kube"},
	})
	cmd, cleanup, err := newCLICommand(ctx, "echo", []string{"hi"}, requestWithCwd(t.TempDir()), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !slices.Contains(cmd.Env, "KUBECONFIG=/tmp/captain-observe-kube") {
		t.Fatalf("env does not contain observation KUBECONFIG: %s", strings.Join(cmd.Env, " "))
	}
}

func TestNewSandboxedCommand_RoutesThroughCommandWrapper(t *testing.T) {
	stub := &wrapperSandboxStub{}
	api.RegisterSandbox(api.SandboxNone, func(api.SandboxConfig) (api.Sandbox, error) { return stub, nil })
	t.Cleanup(func() { api.RegisterSandbox(api.SandboxNone, adapter.None) })

	cmd, cleanup, err := newSandboxedCommand(context.Background(), api.SandboxConfig{Kind: api.SandboxNone}, "claude", []string{"-p"}, requestWithSetupVar(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if got := cmd.Args; len(got) != 4 || got[0] != "confine" || got[1] != "--" || got[2] != "claude" || got[3] != "-p" {
		t.Fatalf("args = %v, want the wrapper's rewrite", got)
	}
	if len(stub.gotEnv) != 1 || stub.gotEnv[0] != "SETUP_VAR=1" {
		t.Fatalf("wrapper saw env %v, want the request-declared setup vars", stub.gotEnv)
	}
	if len(cmd.Env) == 0 || cmd.Env[len(cmd.Env)-1] != "WRAPPED=1" {
		t.Fatalf("env = %v, want the wrapper's env applied", cmd.Env)
	}
	cleanup()
	if !stub.closed {
		t.Fatal("cleanup must close the sandbox")
	}
}

func requestWithCwd(cwd string) *ai.Request {
	return &ai.Request{Setup: &shell.Setup{Cwd: cwd}}
}

// requestWithSetupVar declares SETUP_VAR and carries its resolved value, the
// shape Setup.Resolve produces (declared names in EnvVars, values in Env).
func requestWithSetupVar(cwd string) *ai.Request {
	return &ai.Request{Setup: &shell.Setup{
		Cwd:     cwd,
		EnvVars: []types.EnvVar{{Name: "SETUP_VAR"}},
		Env:     []string{"SETUP_VAR=1"},
	}}
}
