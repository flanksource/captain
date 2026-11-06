// Specs for RemoteProvider: which selections it relocates, how the provider it
// returns presents itself, and how it releases the prepared sandbox.
package promptrun

import (
	"context"
	"errors"

	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	_ "github.com/flanksource/captain/pkg/sandbox/adapter"
)

type remoteSandboxStub struct{ closes int }

func (s *remoteSandboxStub) Kind() api.SandboxKind { return api.SandboxGitAgent }
func (s *remoteSandboxStub) Prepare(context.Context, *api.Spec) (*api.SandboxSession, error) {
	return &api.SandboxSession{}, nil
}
func (s *remoteSandboxStub) Close() error { s.closes++; return nil }

type remoteExecutorStub struct{ err error }

func (s remoteExecutorStub) Execute(context.Context, api.Spec, api.RemoteExecOptions) (*api.Response, error) {
	return nil, s.err
}

var _ = ginkgo.Describe("RemoteProvider", func() {
	// A remote-executing selection must replace provider execution. Without this
	// the run silently falls through to the model provider and the sandbox is a
	// no-op — which is indistinguishable from success until a dispatch never
	// arrives.
	ginkgo.It("routes a git-agent selection to a remote provider that never streams", func() {
		cfg := ai.Config{
			SandboxSelection: &api.SandboxConfig{
				Kind: api.SandboxGitAgent,
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
		provider, err := RemoteProvider(&ai.Request{}, cfg)
		Expect(err).NotTo(HaveOccurred())
		Expect(provider).NotTo(BeNil(), "a git-agent selection must produce a remote-executing provider, not fall through to the model")
		defer closeProvider(provider)
		_, streams := provider.(ai.StreamingProvider)
		Expect(streams).To(BeFalse(), "the wrapped remote provider %T advertises unsupported streaming", provider)
		_, remote := api.ProviderAs[*remoteExecProvider](provider)
		Expect(remote).To(BeTrue(), "the wrapped provider %T hides the remote provider", provider)
	})

	ginkgo.It("leaves local sandboxes alone", func() {
		for _, selection := range []*api.SandboxConfig{
			nil,
			{Kind: api.SandboxNative},
			{Kind: api.SandboxDocker},
		} {
			provider, err := RemoteProvider(&ai.Request{}, ai.Config{SandboxSelection: selection})
			Expect(err).NotTo(HaveOccurred(), "selection %v", selection)
			Expect(provider).To(BeNil(), "selection %v produced %T; only remote-exec kinds replace the provider", selection, provider)
		}
	})

	ginkgo.It("closes the sandbox once, even after a failed run", func() {
		sandbox := &remoteSandboxStub{}
		provider := &remoteExecProvider{executor: remoteExecutorStub{err: errors.New("failed")}, sandbox: sandbox}
		_, err := provider.Execute(context.Background(), ai.Request{})
		Expect(err).To(MatchError("failed"))
		Expect(provider.Close()).To(Succeed())
		Expect(provider.Close()).To(Succeed())
		Expect(sandbox.closes).To(Equal(1))
	})
})
