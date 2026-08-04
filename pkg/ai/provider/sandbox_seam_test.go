package provider

import (
	"context"
	"testing"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/sandbox/adapter"
)

// wrapperSandboxStub is a sandbox whose CommandWrapper prefixes the argv, so
// the test can observe the rewrite at the exec seam.
type wrapperSandboxStub struct{ closed bool }

func (s *wrapperSandboxStub) Kind() api.SandboxKind { return api.SandboxNone }
func (s *wrapperSandboxStub) Prepare(context.Context, *api.Spec) (*api.SandboxSession, error) {
	return &api.SandboxSession{}, nil
}
func (s *wrapperSandboxStub) Close() error { s.closed = true; return nil }
func (s *wrapperSandboxStub) Wrap(_ context.Context, cmd string, args, env []string) (string, []string, []string, error) {
	return "confine", append([]string{"--", cmd}, args...), append(env, "WRAPPED=1"), nil
}

func TestNewCLICommand_UnsandboxedStaysBare(t *testing.T) {
	cmd, cleanup, err := newCLICommand(context.Background(), "echo", []string{"hi"}, t.TempDir(), nil)
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

func TestNewSandboxedCommand_RoutesThroughCommandWrapper(t *testing.T) {
	stub := &wrapperSandboxStub{}
	api.RegisterSandbox(api.SandboxNone, func(api.SandboxConfig) (api.Sandbox, error) { return stub, nil })
	t.Cleanup(func() { api.RegisterSandbox(api.SandboxNone, adapter.None) })

	cmd, cleanup, err := newSandboxedCommand(context.Background(), api.SandboxConfig{Kind: api.SandboxNone}, "claude", []string{"-p"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := cmd.Args; len(got) != 4 || got[0] != "confine" || got[1] != "--" || got[2] != "claude" || got[3] != "-p" {
		t.Fatalf("args = %v, want the wrapper's rewrite", got)
	}
	if len(cmd.Env) == 0 || cmd.Env[len(cmd.Env)-1] != "WRAPPED=1" {
		t.Fatalf("env = %v, want the wrapper's env applied", cmd.Env)
	}
	cleanup()
	if !stub.closed {
		t.Fatal("cleanup must close the sandbox")
	}
}
