package ai

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons/logger"
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

var _ = DescribeTable("model effort resolution",
	func(backend Backend, model string, requested, expected api.Effort) {
		effective, err := ResolveModelEffort(backend, model, requested)

		Expect(err).NotTo(HaveOccurred())
		Expect(effective).To(Equal(expected))
	},
	Entry("keeps a supported tier", BackendOpenAI, "gpt-5.6", api.EffortMax, api.EffortMax),
	Entry("uses the highest known tier", BackendCodexAgent, "gpt-5.5", api.EffortMax, api.EffortXHigh),
	Entry("keeps an uncataloged model permissive", BackendCodexAgent, "gpt-future", api.EffortUltra, api.EffortUltra),
	Entry("omits effort for a model without a reasoning knob", BackendDeepSeek, "deepseek-v4-pro", api.EffortHigh, api.EffortNone),
)

var _ = Describe("model effort runtime validation", func() {
	It("drops configured effort when the model has no reasoning support", func() {
		capture := &effortCaptureProvider{backend: BackendGemini, model: "gemini-2.5-flash"}
		provider := withEffortValidation(capture, api.EffortHigh)

		_, err := provider.Execute(context.Background(), Request{})

		Expect(err).NotTo(HaveOccurred())
		Expect(capture.request.Effort).To(Equal(api.EffortNone))
	})

	It("degrades any unsupported tier on a reasoning model", func() {
		capture := &effortCaptureProvider{backend: BackendOpenAI, model: "gpt-5.5"}
		provider := withEffortValidation(capture, api.EffortMax)

		_, err := provider.Execute(context.Background(), Request{})

		Expect(err).NotTo(HaveOccurred())
		Expect(capture.request.Effort).To(Equal(api.EffortXHigh))
	})

	It("degrades an unsupported effort to the highest supported tier at debug level", func() {
		capture := &effortCaptureProvider{backend: BackendGemini, model: "gemini-3.6-flash"}
		provider := withEffortValidation(capture, api.EffortXHigh)
		logs := logger.NewBufferedLogger(10)
		logs.SetLogLevel("debug")

		_, err := provider.Execute(ContextWithLogger(context.Background(), logs), Request{})

		Expect(err).NotTo(HaveOccurred())
		Expect(capture.request.Effort).To(Equal(api.EffortHigh))
		Expect(logs.GetLogsByLevel(logger.Warn)).To(BeEmpty())
		Expect(logs.GetLogsByLevel(logger.Debug)).To(ConsistOf(
			HaveField("Message", ContainSubstring(`using highest supported effort "high"`)),
		))
	})
})
