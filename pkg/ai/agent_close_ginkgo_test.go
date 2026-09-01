package ai_test

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	captainai "github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

type closeTrackingProvider struct {
	closeCalls int
	closeErr   error
}

func (p *closeTrackingProvider) Execute(context.Context, api.Spec) (*api.Response, error) {
	return &api.Response{}, nil
}

func (p *closeTrackingProvider) GetModel() string { return "test-model" }
func (p *closeTrackingProvider) GetRuntime() api.Runtime {
	return api.RuntimeOf(api.OpenAI, api.ModeAgent)
}
func (p *closeTrackingProvider) Close() error {
	p.closeCalls++
	return p.closeErr
}

type providerWrapper struct {
	inner api.Provider
}

func (w providerWrapper) Execute(ctx context.Context, req api.Spec) (*api.Response, error) {
	return w.inner.Execute(ctx, req)
}

func (w providerWrapper) GetModel() string        { return w.inner.GetModel() }
func (w providerWrapper) GetRuntime() api.Runtime { return w.inner.GetRuntime() }
func (w providerWrapper) Unwrap() api.Provider    { return w.inner }

var _ = Describe("Agent provider lifecycle", func() {
	It("closes a provider through nested wrappers", func() {
		leaf := &closeTrackingProvider{}
		agent := captainai.NewAgentWithProvider(
			providerWrapper{inner: providerWrapper{inner: leaf}},
			captainai.Config{},
		)

		Expect(agent.Close()).To(Succeed())
		Expect(leaf.closeCalls).To(Equal(1))
	})

	It("returns the wrapped provider close error", func() {
		closeErr := errors.New("stop supervised provider")
		leaf := &closeTrackingProvider{closeErr: closeErr}
		agent := captainai.NewAgentWithProvider(providerWrapper{inner: leaf}, captainai.Config{})

		err := agent.Close()

		Expect(err).To(MatchError(closeErr))
		Expect(errors.Is(err, closeErr)).To(BeTrue())
		Expect(leaf.closeCalls).To(Equal(1))
	})
})
