package cli

import (
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/captain/pkg/sandbox/adapter"
)

// A remote-executing selection must replace provider execution. Without this
// the run silently falls through to the model provider and the sandbox is a
// no-op — which is indistinguishable from success until a dispatch never
// arrives.
func TestRemoteExecProviderForRoutesGitAgent(t *testing.T) {
	req := &ai.Request{}
	cfg := ai.Config{
		SandboxSelection: &api.SandboxConfig{
			Kind: registry.SandboxGitAgent,
			Name: "git-agent",
			Options: map[string]any{
				"agents": map[string]any{
					"worker-01": map[string]any{
						"fingerprint":     "SHA256:key",
						"url":             "ssh://captain@127.0.0.1:7502/repo.git",
						"hostFingerprint": "SHA256:host",
					},
				},
			},
		},
	}
	provider, err := remoteExecProviderFor(req, cfg)
	if err != nil {
		t.Fatalf("remoteExecProviderFor: %v", err)
	}
	if provider == nil {
		t.Fatal("a git-agent selection must produce a remote-executing provider, not fall through to the model")
	}
	if _, isRemote := provider.(*remoteExecProvider); !isRemote {
		t.Fatalf("provider = %T, want *remoteExecProvider", provider)
	}
}

// A relocated run waits on a remote agent doing real work. Inheriting the
// local request default kills a dispatch that is still progressing and reports
// it as a failure ("dispatched but not concluded: context deadline exceeded").
func TestRemoteAwareTimeout(t *testing.T) {
	remote := ai.Config{SandboxSelection: &api.SandboxConfig{Kind: registry.SandboxGitAgent}}
	local := ai.Config{SandboxSelection: &api.SandboxConfig{Kind: registry.SandboxSRT}}
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
		for name, cfg := range map[string]ai.Config{"srt": local, "none": {}} {
			if got := remoteAwareTimeout(ai.Request{}, cfg, requestDefault); got != requestDefault {
				t.Fatalf("%s: timeout = %s, want %s", name, got, requestDefault)
			}
		}
	})
}

func TestRemoteExecProviderForLeavesLocalSandboxesAlone(t *testing.T) {
	for _, selection := range []*api.SandboxConfig{
		nil,
		{Kind: registry.SandboxSRT},
		{Kind: registry.SandboxContainer},
	} {
		provider, err := remoteExecProviderFor(&ai.Request{}, ai.Config{SandboxSelection: selection})
		if err != nil {
			t.Fatalf("selection %v: %v", selection, err)
		}
		if provider != nil {
			t.Fatalf("selection %v produced %T; only remote-exec kinds replace the provider", selection, provider)
		}
	}
}
