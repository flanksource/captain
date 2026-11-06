package promptrun

import (
	"context"
	"fmt"

	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

type bufferedOnlyProvider struct{ executeCalls int }

func (p *bufferedOnlyProvider) GetModel() string { return "buffered-model" }
func (p *bufferedOnlyProvider) GetRuntime() ai.Runtime {
	return api.RuntimeOf(api.DeepSeek, api.ModeAPI)
}
func (p *bufferedOnlyProvider) Execute(context.Context, ai.Request) (*ai.Response, error) {
	p.executeCalls++
	return &ai.Response{
		Text:           "done",
		StructuredData: map[string]any{"status": "ok"},
		Model:          "buffered-model",
		Runtime:        api.RuntimeOf(api.DeepSeek, api.ModeAPI),
		Usage:          ai.Usage{InputTokens: 2, OutputTokens: 3},
		CostUSD:        0.01,
	}, nil
}

type streamingProvider struct {
	bufferedOnlyProvider
	streamCalls int
}

type rejectedBufferedProvider struct{ bufferedOnlyProvider }

func (p *rejectedBufferedProvider) Execute(ctx context.Context, _ ai.Request) (*ai.Response, error) {
	ai.ReportEvent(ctx, ai.Event{Kind: ai.EventText, Text: "# Initial findings", Rejected: true})
	return &ai.Response{Text: "# Final findings", Usage: ai.Usage{OutputTokens: 30}, CostUSD: 0.75}, fmt.Errorf("%w: expected JSON", ai.ErrSchemaValidation)
}

func (p *streamingProvider) ExecuteStream(context.Context, ai.Request) (<-chan ai.Event, error) {
	p.streamCalls++
	events := make(chan ai.Event, 1)
	events <- ai.Event{Kind: ai.EventResult, Success: true}
	close(events)
	return events, nil
}

func drain(events <-chan ai.Event) []ai.Event {
	var got []ai.Event
	for event := range events {
		got = append(got, event)
	}
	return got
}

var _ = ginkgo.Describe("runnerProvider", func() {
	ginkgo.It("keeps buffered rejection diagnostics before the terminal error and its spend", func() {
		runner, err := runnerProvider(&rejectedBufferedProvider{}, true, false)
		Expect(err).NotTo(HaveOccurred())
		events, err := runner.ExecuteStream(context.Background(), ai.Request{})
		Expect(err).NotTo(HaveOccurred())
		Expect(drain(events)).To(HaveExactElements(
			And(HaveField("Kind", ai.EventText), HaveField("Text", "# Initial findings"), HaveField("Rejected", true)),
			And(HaveField("Kind", ai.EventText), HaveField("Text", "# Final findings"), HaveField("Rejected", true)),
			And(HaveField("Kind", ai.EventError), HaveField("Error", ContainSubstring(ai.ErrSchemaValidation.Error())),
				HaveField("Usage", &ai.Usage{OutputTokens: 30}), HaveField("CostUSD", 0.75)),
		))
	})

	ginkgo.It("replays a buffered-only provider's response as completed events", func() {
		provider := &bufferedOnlyProvider{}
		runner, err := runnerProvider(provider, false, false)
		Expect(err).NotTo(HaveOccurred())

		events, err := runner.ExecuteStream(context.Background(), ai.Request{})
		Expect(err).NotTo(HaveOccurred())
		got := drain(events)
		Expect(provider.executeCalls).To(Equal(1))
		Expect(got).To(HaveLen(2))
		Expect(got[0].Kind).To(Equal(ai.EventText))
		Expect(got[0].Text).To(Equal("done"))
		Expect(got[1].Kind).To(Equal(ai.EventResult))
		Expect(string(got[1].StructuredData)).To(Equal(`{"status":"ok"}`))
		Expect(got[1].Usage.InputTokens).To(Equal(2))
		Expect(got[1].CostUSD).To(Equal(0.01))
	})

	ginkgo.It("forces a streaming provider through Execute under NoStream", func() {
		provider := &streamingProvider{}
		runner, err := runnerProvider(provider, true, false)
		Expect(err).NotTo(HaveOccurred())

		events, err := runner.ExecuteStream(context.Background(), ai.Request{})
		Expect(err).NotTo(HaveOccurred())
		drain(events)
		Expect(provider.executeCalls).To(Equal(1))
		Expect(provider.streamCalls).To(BeZero())
	})

	ginkgo.It("leaves a streaming provider alone otherwise", func() {
		provider := &streamingProvider{}
		runner, err := runnerProvider(provider, false, false)
		Expect(err).NotTo(HaveOccurred())

		events, err := runner.ExecuteStream(context.Background(), ai.Request{})
		Expect(err).NotTo(HaveOccurred())
		drain(events)
		Expect(provider.streamCalls).To(Equal(1))
		Expect(provider.executeCalls).To(BeZero())
	})

	ginkgo.It("needs no provider for a verify-only run and refuses to generate without one", func() {
		runner, err := runnerProvider(nil, false, true)
		Expect(err).NotTo(HaveOccurred())
		Expect(runner).To(BeNil())

		_, err = runnerProvider(nil, false, false)
		Expect(err).To(MatchError(ContainSubstring("needs a provider")))
	})
})
