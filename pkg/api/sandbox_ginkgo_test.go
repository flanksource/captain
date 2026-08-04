package api_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

type sandboxStub struct{ kind api.SandboxKind }

func (s sandboxStub) Kind() api.SandboxKind { return s.kind }
func (s sandboxStub) Prepare(context.Context, *api.Spec) (*api.SandboxSession, error) {
	return &api.SandboxSession{}, nil
}
func (s sandboxStub) Close() error { return nil }

type wrappingSandboxStub struct{ sandboxStub }

func (wrappingSandboxStub) Wrap(_ context.Context, cmd string, args, env []string) (string, []string, []string, error) {
	return "wrapped-" + cmd, args, env, nil
}

type sandboxDecorator struct{ inner api.Sandbox }

func (d sandboxDecorator) Kind() api.SandboxKind { return d.inner.Kind() }
func (d sandboxDecorator) Prepare(ctx context.Context, spec *api.Spec) (*api.SandboxSession, error) {
	return d.inner.Prepare(ctx, spec)
}
func (d sandboxDecorator) Close() error        { return d.inner.Close() }
func (d sandboxDecorator) Unwrap() api.Sandbox { return d.inner }

var _ = Describe("SandboxAs", func() {
	It("finds a capability through nested decorators", func() {
		sandbox := sandboxDecorator{inner: sandboxDecorator{inner: wrappingSandboxStub{sandboxStub{kind: api.SandboxSRT}}}}

		wrapper, ok := api.SandboxAs[api.CommandWrapper](sandbox)

		Expect(ok).To(BeTrue())
		cmd, _, _, err := wrapper.Wrap(context.Background(), "claude", nil, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(cmd).To(Equal("wrapped-claude"))
	})

	It("reports absence when no layer implements the capability", func() {
		sandbox := sandboxDecorator{inner: sandboxStub{kind: api.SandboxNone}}

		_, ok := api.SandboxAs[api.RemoteExecutor](sandbox)

		Expect(ok).To(BeFalse())
	})
})

var _ = Describe("NewSandbox", func() {
	It("constructs the registered adapter for a kind", func() {
		api.RegisterSandbox(api.SandboxNone, func(cfg api.SandboxConfig) (api.Sandbox, error) {
			return sandboxStub{kind: api.SandboxNone}, nil
		})

		sandbox, err := api.NewSandbox(api.SandboxConfig{Kind: api.SandboxNone})

		Expect(err).NotTo(HaveOccurred())
		Expect(sandbox.Kind()).To(Equal(api.SandboxNone))
	})

	It("defaults an empty kind to none", func() {
		api.RegisterSandbox(api.SandboxNone, func(cfg api.SandboxConfig) (api.Sandbox, error) {
			return sandboxStub{kind: api.SandboxNone}, nil
		})

		sandbox, err := api.NewSandbox(api.SandboxConfig{})

		Expect(err).NotTo(HaveOccurred())
		Expect(sandbox.Kind()).To(Equal(api.SandboxNone))
	})

	It("rejects an unknown kind, naming the valid set", func() {
		_, err := api.NewSandbox(api.SandboxConfig{Kind: "warp-drive"})

		Expect(err).To(MatchError(ContainSubstring(`unknown sandbox kind "warp-drive"`)))
		Expect(err).To(MatchError(ContainSubstring("none, srt, container, git-agent")))
	})

	It("rejects a known kind with no registered adapter", func() {
		_, err := api.NewSandbox(api.SandboxConfig{Kind: api.SandboxGitAgent})

		Expect(err).To(MatchError(ContainSubstring(`no sandbox adapter registered for kind "git-agent"`)))
	})

	It("rejects an adapter that does not implement a declared capability", func() {
		api.RegisterSandbox(api.SandboxSRT, func(cfg api.SandboxConfig) (api.Sandbox, error) {
			return sandboxStub{kind: api.SandboxSRT}, nil // srt declares wrap-command; stub lacks Wrap
		})

		_, err := api.NewSandbox(api.SandboxConfig{Kind: api.SandboxSRT})

		Expect(err).To(MatchError(ContainSubstring(`declares capability "wrap-command"`)))
	})

	It("accepts an adapter that implements its declared capabilities", func() {
		api.RegisterSandbox(api.SandboxSRT, func(cfg api.SandboxConfig) (api.Sandbox, error) {
			return wrappingSandboxStub{sandboxStub{kind: api.SandboxSRT}}, nil
		})

		sandbox, err := api.NewSandbox(api.SandboxConfig{Kind: api.SandboxSRT})

		Expect(err).NotTo(HaveOccurred())
		_, ok := api.SandboxAs[api.CommandWrapper](sandbox)
		Expect(ok).To(BeTrue())
	})
})

var _ = Describe("RegisterSandbox", func() {
	It("panics on a kind with no descriptor", func() {
		Expect(func() {
			api.RegisterSandbox("warp-drive", func(cfg api.SandboxConfig) (api.Sandbox, error) {
				return nil, nil
			})
		}).To(PanicWith(ContainSubstring(`kind "warp-drive" has no descriptor`)))
	})
})
