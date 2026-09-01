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
		sandbox := sandboxDecorator{inner: sandboxDecorator{inner: wrappingSandboxStub{sandboxStub{kind: api.SandboxDocker}}}}

		wrapper, ok := api.SandboxAs[api.CommandWrapper](sandbox)

		Expect(ok).To(BeTrue())
		cmd, _, _, err := wrapper.Wrap(context.Background(), "claude", nil, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(cmd).To(Equal("wrapped-claude"))
	})

	It("reports absence when no layer implements the capability", func() {
		sandbox := sandboxDecorator{inner: sandboxStub{kind: api.SandboxOff}}

		_, ok := api.SandboxAs[api.RemoteExecutor](sandbox)

		Expect(ok).To(BeFalse())
	})
})

var _ = Describe("NewSandbox", func() {
	It("constructs the registered adapter for a kind", func() {
		api.RegisterSandbox(api.SandboxOff, func(cfg api.SandboxConfig) (api.Sandbox, error) {
			return sandboxStub{kind: api.SandboxOff}, nil
		})

		sandbox, err := api.NewSandbox(api.SandboxConfig{Kind: api.SandboxOff})

		Expect(err).NotTo(HaveOccurred())
		Expect(sandbox.Kind()).To(Equal(api.SandboxOff))
	})

	It("defaults an empty kind to off", func() {
		api.RegisterSandbox(api.SandboxOff, func(cfg api.SandboxConfig) (api.Sandbox, error) {
			return sandboxStub{kind: api.SandboxOff}, nil
		})

		sandbox, err := api.NewSandbox(api.SandboxConfig{})

		Expect(err).NotTo(HaveOccurred())
		Expect(sandbox.Kind()).To(Equal(api.SandboxOff))
	})

	It("rejects an unknown kind, naming the valid set", func() {
		_, err := api.NewSandbox(api.SandboxConfig{Kind: "warp-drive"})

		Expect(err).To(MatchError(ContainSubstring(`unknown sandbox kind "warp-drive"`)))
		Expect(err).To(MatchError(ContainSubstring("off, native, docker, git-agent")))
	})

	It("rejects a known kind with no registered adapter", func() {
		_, err := api.NewSandbox(api.SandboxConfig{Kind: api.SandboxGitAgent})

		Expect(err).To(MatchError(ContainSubstring(`no sandbox adapter registered for kind "git-agent"`)))
	})

	It("rejects an adapter that does not implement a declared capability", func() {
		api.RegisterSandbox(api.SandboxDocker, func(cfg api.SandboxConfig) (api.Sandbox, error) {
			return sandboxStub{kind: api.SandboxDocker}, nil
		})

		_, err := api.NewSandbox(api.SandboxConfig{Kind: api.SandboxDocker})

		Expect(err).To(MatchError(ContainSubstring(`declares capability "wrap-command"`)))
	})

	It("rejects a declared capability with no construction verifier", func() {
		descriptor, ok := api.SandboxFor(api.SandboxDocker)
		Expect(ok).To(BeTrue())
		original := append([]api.SandboxCapability(nil), descriptor.Capabilities...)
		descriptor.Capabilities = append(descriptor.Capabilities, api.SandboxCapability("future-capability"))
		DeferCleanup(func() { descriptor.Capabilities = original })
		api.RegisterSandbox(api.SandboxDocker, func(cfg api.SandboxConfig) (api.Sandbox, error) {
			return wrappingSandboxStub{sandboxStub{kind: api.SandboxDocker}}, nil
		})

		_, err := api.NewSandbox(api.SandboxConfig{Kind: api.SandboxDocker})

		Expect(err).To(MatchError(ContainSubstring(`capability "future-capability" but no construction-time verifier`)))
	})

	It("rejects an adapter that fails a declared capability's verifier", func() {
		descriptor, ok := api.SandboxFor(api.SandboxDocker)
		Expect(ok).To(BeTrue())
		original := append([]api.SandboxCapability(nil), descriptor.Capabilities...)
		descriptor.Capabilities = append(descriptor.Capabilities, api.CapabilityEgressProxy)
		DeferCleanup(func() { descriptor.Capabilities = original })
		api.RegisterSandbox(api.SandboxDocker, func(cfg api.SandboxConfig) (api.Sandbox, error) {
			return wrappingSandboxStub{sandboxStub{kind: api.SandboxDocker}}, nil
		})

		_, err := api.NewSandbox(api.SandboxConfig{Kind: api.SandboxDocker})

		Expect(err).To(MatchError(ContainSubstring(`declares capability "egress-proxy" but its adapter does not implement it`)))
	})

	It("accepts an adapter that implements its declared capabilities", func() {
		api.RegisterSandbox(api.SandboxDocker, func(cfg api.SandboxConfig) (api.Sandbox, error) {
			return wrappingSandboxStub{sandboxStub{kind: api.SandboxDocker}}, nil
		})

		sandbox, err := api.NewSandbox(api.SandboxConfig{Kind: api.SandboxDocker})

		Expect(err).NotTo(HaveOccurred())
		_, ok := api.SandboxAs[api.CommandWrapper](sandbox)
		Expect(ok).To(BeTrue())
	})
})

var _ = Describe("NewProvider sandbox validation", func() {
	It("rejects an unsupported sandbox × backend pairing before construction", func() {
		_, err := api.NewProvider(api.Config{
			Model:            api.Model{Name: "claude-sonnet-5", Mode: api.ModeAgent},
			SandboxSelection: &api.SandboxConfig{Kind: api.SandboxDocker},
		})

		Expect(err).To(MatchError(ContainSubstring("does not support runtime mode")))
	})

	It("accepts off for agent backends", func() {
		_, err := api.NewProvider(api.Config{
			Model:            api.Model{Name: "claude-sonnet-5", Mode: api.ModeAgent},
			SandboxSelection: &api.SandboxConfig{Kind: api.SandboxOff},
		})

		Expect(err).NotTo(MatchError(ContainSubstring("does not support runtime mode")))
	})

	It("rejects a factory returning a nil instance", func() {
		api.RegisterSandbox(api.SandboxOff, func(api.SandboxConfig) (api.Sandbox, error) { return nil, nil })

		_, err := api.NewSandbox(api.SandboxConfig{Kind: api.SandboxOff})

		Expect(err).To(MatchError(ContainSubstring("returned no instance")))
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
