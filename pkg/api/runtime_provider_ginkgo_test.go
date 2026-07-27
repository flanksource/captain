package api_test

import (
	"context"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/credentials"
)

type runtimeProviderStub struct{}

func (runtimeProviderStub) Execute(context.Context, api.Spec) (*api.Response, error) {
	return &api.Response{}, nil
}
func (runtimeProviderStub) GetModel() string                { return "test-model" }
func (runtimeProviderStub) GetBackend() api.Backend         { return api.BackendClaudeAgent }
func (runtimeProviderStub) Interrupt(context.Context) error { return nil }

type runtimeProviderWrapper struct{ inner api.Provider }

func (w runtimeProviderWrapper) Execute(ctx context.Context, req api.Spec) (*api.Response, error) {
	return w.inner.Execute(ctx, req)
}
func (w runtimeProviderWrapper) GetModel() string        { return w.inner.GetModel() }
func (w runtimeProviderWrapper) GetBackend() api.Backend { return w.inner.GetBackend() }
func (w runtimeProviderWrapper) Unwrap() api.Provider    { return w.inner }

var _ = Describe("ProviderAs", func() {
	It("finds a provider capability through nested wrappers", func() {
		provider := runtimeProviderWrapper{inner: runtimeProviderWrapper{inner: runtimeProviderStub{}}}

		interruptible, ok := api.ProviderAs[api.InterruptibleProvider](provider)

		Expect(ok).To(BeTrue())
		Expect(interruptible.Interrupt(context.Background())).To(Succeed())
	})

	It("returns false for nil and missing capabilities", func() {
		var provider api.Provider

		_, ok := api.ProviderAs[api.InterruptibleProvider](provider)
		Expect(ok).To(BeFalse())

		_, ok = api.ProviderAs[api.CloseableProvider](runtimeProviderStub{})
		Expect(ok).To(BeFalse())
	})
})

var _ = Describe("API key resolution", func() {
	BeforeEach(func() {
		credentials.SetPathForTesting(filepath.Join(GinkgoT().TempDir(), "vault"))
		DeferCleanup(func() { credentials.SetPathForTesting("") })
		GinkgoT().Setenv("OPENAI_API_KEY", "environment-token")
	})

	It("prefers the Captain vault over the environment", func() {
		vault, err := credentials.DefaultVault()
		Expect(err).NotTo(HaveOccurred())
		Expect(vault.Set("openai", "vault-token")).To(Succeed())

		resolved, err := api.ResolveAPIKey(api.BackendOpenAI)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Token).To(Equal("vault-token"))
		Expect(resolved.Source).To(Equal(credentials.SourceVault))
	})

	It("falls back to the environment", func() {
		resolved, err := api.ResolveAPIKey(api.BackendOpenAI)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Token).To(Equal("environment-token"))
		Expect(resolved.Source).To(Equal(credentials.SourceEnvironment))
		Expect(resolved.Detail).To(Equal("OPENAI_API_KEY"))
	})
})
