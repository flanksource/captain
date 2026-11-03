package cli

import (
	"context"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/captain/pkg/sandbox/adapter"
)

// relocatingSandboxStub is a remote-exec adapter that answers the run itself,
// so a test can tell a relocated run from one built as a local provider.
type relocatingSandboxStub struct{ executed int }

func (s *relocatingSandboxStub) Kind() api.SandboxKind { return registry.SandboxGitAgent }
func (s *relocatingSandboxStub) Prepare(context.Context, *api.Spec) (*api.SandboxSession, error) {
	return &api.SandboxSession{}, nil
}
func (s *relocatingSandboxStub) Close() error              { return nil }
func (s *relocatingSandboxStub) IsolatesWorkspace() bool   { return true }
func (s *relocatingSandboxStub) ProvidesEgressProxy() bool { return true }
func (s *relocatingSandboxStub) Execute(context.Context, api.Spec) (*api.Response, error) {
	s.executed++
	return &api.Response{Text: "done remotely"}, nil
}

// `captain ai` builds its provider outside promptrun, so it must take the
// remote branch itself: a git-agent selection is served by the adapter, never
// by a local, streaming provider.
func TestBuildProviderRelocatesRemoteSandboxes(t *testing.T) {
	stub := &relocatingSandboxStub{}
	api.RegisterSandbox(registry.SandboxGitAgent, func(api.SandboxConfig) (api.Sandbox, error) { return stub, nil })
	t.Cleanup(func() { api.RegisterSandbox(registry.SandboxGitAgent, adapter.GitAgent) })

	req := &ai.Request{}
	cfg := ai.Config{SandboxSelection: &api.SandboxConfig{Kind: registry.SandboxGitAgent}}
	provider, cleanup, err := buildProvider(context.Background(), req, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	defer closeProvider(provider)
	if _, ok := provider.(ai.StreamingProvider); ok {
		t.Fatalf("remote provider %T advertises unsupported streaming", provider)
	}
	resp, err := provider.Execute(context.Background(), *req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "done remotely" || stub.executed != 1 {
		t.Fatalf("response %q after %d remote executions; want the adapter to serve the run once", resp.Text, stub.executed)
	}
}

// A relocated run waits on a remote agent doing real work. Inheriting the
// local request default kills a dispatch that is still progressing and reports
// it as a failure ("dispatched but not concluded: context deadline exceeded").
func TestRemoteAwareTimeout(t *testing.T) {
	remote := ai.Config{SandboxSelection: &api.SandboxConfig{Kind: registry.SandboxGitAgent}}
	local := ai.Config{SandboxSelection: &api.SandboxConfig{Kind: registry.SandboxDocker}}
	const requestDefault = 120 * time.Second

	t.Run("a relocated run waits for the agent, not the model", func(t *testing.T) {
		got := remoteAwareTimeout(ai.Request{}, remote, requestDefault)
		if got != adapter.DefaultWaitTimeout {
			t.Fatalf("timeout = %s, want the remote wait budget %s", got, adapter.DefaultWaitTimeout)
		}
	})

	t.Run("the backend's waitTimeout is honoured", func(t *testing.T) {
		scoped := ai.Config{SandboxSelection: &api.SandboxConfig{
			Kind:    registry.SandboxGitAgent,
			Options: map[string]any{"waitTimeout": "15m"},
		}}
		if got := remoteAwareTimeout(ai.Request{}, scoped, requestDefault); got != 15*time.Minute {
			t.Fatalf("timeout = %s, want 15m", got)
		}
	})

	t.Run("an explicit timeout always wins", func(t *testing.T) {
		req := ai.Request{}
		req.Budget.Timeout = "45s"
		// runContext applies the declared budget itself; the point here is that
		// the remote default does not override an explicit choice.
		if got := remoteAwareTimeout(req, remote, requestDefault); got != requestDefault {
			t.Fatalf("timeout = %s, want the caller's %s left untouched", got, requestDefault)
		}
	})

	t.Run("local sandboxes keep the request default", func(t *testing.T) {
		for name, cfg := range map[string]ai.Config{"docker": local, "off": {}} {
			if got := remoteAwareTimeout(ai.Request{}, cfg, requestDefault); got != requestDefault {
				t.Fatalf("%s: timeout = %s, want %s", name, got, requestDefault)
			}
		}
	})
}
