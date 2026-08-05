package cli

import (
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
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
