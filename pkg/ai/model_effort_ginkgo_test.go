package ai

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

type effortCaptureProvider struct {
	backend Backend
	model   string
	request Request
}

func (p *effortCaptureProvider) Execute(_ context.Context, req Request) (*Response, error) {
	p.request = req
	return &Response{}, nil
}

func (p *effortCaptureProvider) GetModel() string    { return p.model }
func (p *effortCaptureProvider) GetBackend() Backend { return p.backend }

var _ = Describe("model effort runtime validation", func() {
	It("drops configured effort when the model has no reasoning support", func() {
		capture := &effortCaptureProvider{backend: BackendGemini, model: "gemini-2.5-flash"}
		provider := withEffortValidation(capture, api.EffortHigh)

		_, err := provider.Execute(context.Background(), Request{})

		Expect(err).NotTo(HaveOccurred())
		Expect(capture.request.Effort).To(Equal(api.EffortNone))
	})

	It("still rejects an unsupported tier on a reasoning model", func() {
		capture := &effortCaptureProvider{backend: BackendOpenAI, model: "gpt-5.5"}
		provider := withEffortValidation(capture, api.EffortMax)

		_, err := provider.Execute(context.Background(), Request{})

		Expect(err).To(MatchError(ContainSubstring("does not support reasoning effort")))
	})
})
