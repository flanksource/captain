package middleware

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/shell"
	"github.com/flanksource/commons-db/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type observedStub struct {
	stubProvider
	err    error
	events chan ai.Event
}

func (s *observedStub) Execute(context.Context, ai.Request) (*ai.Response, error) {
	return s.resp, s.err
}
func (s *observedStub) ExecuteStream(context.Context, ai.Request) (<-chan ai.Event, error) {
	return s.events, s.err
}

var _ = Describe("provider observers", func() {
	It("publishes request and response including usage, cost and errors until released", func() {
		failure := errors.New("provider failed")
		response := &ai.Response{Usage: ai.Usage{InputTokens: 12}, CostUSD: 0.25}
		p, err := Wrap(&observedStub{stubProvider: stubProvider{resp: response}, err: failure}, WithObservers())
		Expect(err).NotTo(HaveOccurred())
		var events []ProviderEvent
		release := ObserveProvider(func(ev ProviderEvent) { events = append(events, ev) })
		defer release()
		_, err = p.Execute(context.Background(), ai.Request{})
		Expect(err).To(MatchError(failure))
		Expect(events).To(HaveLen(2))
		Expect(events[0].Phase).To(Equal("request"))
		Expect(events[1].Phase).To(Equal("response"))
		Expect(events[1].Model).To(Equal("test-model"))
		Expect(events[1].Backend).NotTo(BeEmpty())
		Expect(events[1].Usage).To(Equal(response.Usage))
		Expect(events[1].Cost).To(Equal(response.CostUSD))
		Expect(events[1].Duration).To(BeNumerically(">", 0))
		Expect(events[1].Err).To(MatchError(failure))
		release()
		_, _ = p.Execute(context.Background(), ai.Request{})
		Expect(events).To(HaveLen(2))
	})
	It("forwards stream events before completion and publishes their aggregate", func() {
		input := make(chan ai.Event)
		p, err := Wrap(&observedStub{events: input}, WithObservers())
		Expect(err).NotTo(HaveOccurred())
		observed := make(chan ProviderEvent, 2)
		release := ObserveProvider(func(ev ProviderEvent) { observed <- ev })
		defer release()
		stream, err := p.(ai.StreamingProvider).ExecuteStream(context.Background(), ai.Request{})
		Expect(err).NotTo(HaveOccurred())
		Expect((<-observed).Phase).To(Equal("request"))
		go func() { input <- ai.Event{Kind: ai.EventText, Text: "hello"} }()
		Expect((<-stream).Text).To(Equal("hello"))
		go func() {
			input <- ai.Event{Kind: ai.EventResult, Success: true, Usage: &ai.Usage{OutputTokens: 3}, CostUSD: 0.5}
			close(input)
		}()
		Expect((<-stream).Kind).To(Equal(ai.EventResult))
		for range stream {
		}
		result := <-observed
		Expect(result.Response.Text).To(Equal("hello"))
		Expect(result.Usage.OutputTokens).To(Equal(3))
		Expect(result.Cost).To(Equal(0.5))
	})
	It("recovers observer panics and does not allocate when unobserved", func() {
		release := ObserveProvider(func(ProviderEvent) { panic("observer failure") })
		Expect(func() { PublishProvider(ProviderEvent{}) }).NotTo(Panic())
		release()
		Expect(testing.AllocsPerRun(100, func() { PublishProvider(ProviderEvent{}) })).To(BeZero())
	})
	It("reports a stream startup failure", func() {
		failure := errors.New("stream unavailable")
		p, err := Wrap(&observedStub{err: failure}, WithObservers())
		Expect(err).NotTo(HaveOccurred())
		var events []ProviderEvent
		release := ObserveProvider(func(ev ProviderEvent) { events = append(events, ev) })
		defer release()
		_, err = p.(ai.StreamingProvider).ExecuteStream(context.Background(), ai.Request{})
		Expect(err).To(MatchError(failure))
		Expect(events).To(HaveLen(2))
		Expect(events[1].Err).To(MatchError(failure))
	})
	It("includes observation in the default stack", func() {
		p, err := Wrap(stubProvider{resp: &ai.Response{}}, DefaultOptions(ai.Config{NoCache: true}, ai.RuntimeOf(ai.Anthropic, ai.ModeAPI))...)
		Expect(err).NotTo(HaveOccurred())
		var events []ProviderEvent
		release := ObserveProvider(func(ev ProviderEvent) { events = append(events, ev) })
		defer release()
		_, err = p.Execute(context.Background(), ai.Request{})
		Expect(err).NotTo(HaveOccurred())
		Expect(events).To(HaveLen(2))
	})
	It("redacts environment values in an independent request for buffered and streamed calls", func() {
		input := make(chan ai.Event)
		close(input)
		p, err := Wrap(&observedStub{stubProvider: stubProvider{resp: &ai.Response{}}, events: input}, WithObservers())
		Expect(err).NotTo(HaveOccurred())
		observed := make(chan ProviderEvent, 4)
		release := ObserveProvider(func(ev ProviderEvent) { observed <- ev })
		defer release()
		req := ai.Request{Setup: &shell.Setup{Env: []string{"TOKEN=secret"}, EnvVars: []types.EnvVar{{Name: "PASSWORD", ValueStatic: "hidden"}}}, Labels: map[string]string{"key": "original"}}
		_, err = p.Execute(context.Background(), req)
		Expect(err).NotTo(HaveOccurred())
		stream, err := p.(ai.StreamingProvider).ExecuteStream(context.Background(), req)
		Expect(err).NotTo(HaveOccurred())
		for range stream {
		}
		for i := 0; i < 4; i++ {
			ev := <-observed
			if ev.Phase != "request" {
				continue
			}
			Expect(ev.Request.Setup.Env).To(Equal([]string{"TOKEN=[REDACTED]"}))
			Expect(ev.Request.Setup.EnvVars[0].Name).To(Equal("PASSWORD"))
			Expect(ev.Request.Setup.EnvVars[0].ValueStatic).To(Equal("[REDACTED]"))
			ev.Request.Labels["key"] = "changed"
		}
		Expect(req.Setup.Env[0]).To(Equal("TOKEN=secret"))
		Expect(req.Setup.EnvVars[0].ValueStatic).To(Equal("hidden"))
		Expect(req.Labels["key"]).To(Equal("original"))
	})
	It("drains a cancelled stream and forwards a waiting terminal result", func() {
		input := make(chan ai.Event)
		producerDone := make(chan struct{})
		p, err := Wrap(&observedStub{events: input}, WithObservers())
		Expect(err).NotTo(HaveOccurred())
		release := ObserveProvider(func(ProviderEvent) {})
		defer release()
		ctx, cancel := context.WithCancel(context.Background())
		stream, err := p.(ai.StreamingProvider).ExecuteStream(ctx, ai.Request{})
		Expect(err).NotTo(HaveOccurred())
		terminal := make(chan ai.Event, 1)
		go func() {
			for ev := range stream {
				terminal <- ev
			}
		}()
		cancel()
		go func() { input <- ai.Event{Kind: ai.EventResult, Success: true}; close(input); close(producerDone) }()
		Eventually(producerDone, time.Second).Should(BeClosed())
		Eventually(terminal, time.Second).Should(Receive(WithTransform(func(ev ai.Event) ai.EventKind { return ev.Kind }, Equal(ai.EventResult))))
	})
	It("drains a cancelled stream even when its caller stops reading", func() {
		input := make(chan ai.Event)
		producerDone := make(chan struct{})
		p, err := Wrap(&observedStub{events: input}, WithObservers())
		Expect(err).NotTo(HaveOccurred())
		release := ObserveProvider(func(ProviderEvent) {})
		defer release()
		ctx, cancel := context.WithCancel(context.Background())
		_, err = p.(ai.StreamingProvider).ExecuteStream(ctx, ai.Request{})
		Expect(err).NotTo(HaveOccurred())
		cancel()
		go func() {
			input <- ai.Event{Kind: ai.EventText, Text: "partial"}
			input <- ai.Event{Kind: ai.EventError, Error: "cancelled"}
			close(input)
			close(producerDone)
		}()
		Eventually(producerDone, time.Second).Should(BeClosed())
	})
	It("forwards a terminal error after cancellation when the caller reads", func() {
		input := make(chan ai.Event)
		p, err := Wrap(&observedStub{events: input}, WithObservers())
		Expect(err).NotTo(HaveOccurred())
		release := ObserveProvider(func(ProviderEvent) {})
		defer release()
		ctx, cancel := context.WithCancel(context.Background())
		stream, err := p.(ai.StreamingProvider).ExecuteStream(ctx, ai.Request{})
		Expect(err).NotTo(HaveOccurred())
		cancel()
		go func() { input <- ai.Event{Kind: ai.EventError, Error: "cancelled"}; close(input) }()
		var event ai.Event
		Eventually(stream, time.Second).Should(Receive(&event))
		Expect(event.Kind).To(Equal(ai.EventError))
		Expect(event.Error).To(Equal("cancelled"))
	})
	It("does not send a response to a listener registered mid-call", func() {
		input := make(chan ai.Event)
		p, err := Wrap(&observedStub{events: input}, WithObservers())
		Expect(err).NotTo(HaveOccurred())
		first := make(chan ProviderEvent, 2)
		release := ObserveProvider(func(ev ProviderEvent) { first <- ev })
		defer release()
		stream, err := p.(ai.StreamingProvider).ExecuteStream(context.Background(), ai.Request{})
		Expect(err).NotTo(HaveOccurred())
		<-first
		late := make(chan ProviderEvent, 1)
		lateRelease := ObserveProvider(func(ev ProviderEvent) { late <- ev })
		defer lateRelease()
		close(input)
		for range stream {
		}
		Eventually(first, time.Second).Should(Receive())
		Consistently(late, 50*time.Millisecond).ShouldNot(Receive())
	})
	It("closes the stream before delivering the response", func() {
		input := make(chan ai.Event)
		p, err := Wrap(&observedStub{events: input}, WithObservers())
		Expect(err).NotTo(HaveOccurred())
		entered, unblock := make(chan struct{}), make(chan struct{})
		release := ObserveProvider(func(ev ProviderEvent) {
			if ev.Phase == "response" {
				close(entered)
				<-unblock
			}
		})
		defer release()
		defer close(unblock)
		stream, err := p.(ai.StreamingProvider).ExecuteStream(context.Background(), ai.Request{})
		Expect(err).NotTo(HaveOccurred())
		close(input)
		Eventually(entered, time.Second).Should(BeClosed())
		Eventually(stream, time.Second).Should(BeClosed())
	})
	It("observes only cache misses in the default stack", func() {
		inner := &scriptedProvider{responses: []string{"answer"}}
		cfg := ai.Config{CacheTTL: time.Hour, CacheDBPath: filepath.Join(GinkgoT().TempDir(), "cache.db")}
		p, err := Wrap(inner, DefaultOptions(cfg, inner.GetRuntime())...)
		Expect(err).NotTo(HaveOccurred())
		var events []ProviderEvent
		release := ObserveProvider(func(ev ProviderEvent) { events = append(events, ev) })
		defer release()
		req := ai.Request{Prompt: api.Prompt{User: "cached"}}
		_, err = p.Execute(context.Background(), req)
		Expect(err).NotTo(HaveOccurred())
		resp, err := p.Execute(context.Background(), req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.CacheHit).To(BeTrue())
		Expect(events).To(HaveLen(2))
	})
	It("observes the initial generation and each schema repair separately", func() {
		inner := &scriptedProvider{responses: []string{overCapJSON, validJSON}}
		p, err := Wrap(inner, DefaultOptions(ai.Config{NoCache: true}, inner.GetRuntime())...)
		Expect(err).NotTo(HaveOccurred())
		var events []ProviderEvent
		release := ObserveProvider(func(ev ProviderEvent) { events = append(events, ev) })
		defer release()
		_, err = p.Execute(context.Background(), capRequest(api.SchemaStrictnessRetry))
		Expect(err).NotTo(HaveOccurred())
		Expect(events).To(HaveLen(4))
		Expect(events[2].Request.Prompt.User).To(ContainSubstring(overCapJSON))
	})
	It("observes both streamed generation and streamed schema repair", func() {
		inner := &streamingProvider{results: []string{overCapJSON, validJSON}}
		p, err := Wrap(inner, DefaultOptions(ai.Config{NoCache: true}, inner.GetRuntime())...)
		Expect(err).NotTo(HaveOccurred())
		observed := make(chan ProviderEvent, 4)
		release := ObserveProvider(func(ev ProviderEvent) { observed <- ev })
		defer release()
		stream, err := p.(ai.StreamingProvider).ExecuteStream(context.Background(), capRequest(api.SchemaStrictnessRetry))
		Expect(err).NotTo(HaveOccurred())
		for range stream {
		}
		var requests, responses int
		for i := 0; i < 4; i++ {
			var ev ProviderEvent
			Eventually(observed, time.Second).Should(Receive(&ev))
			if ev.Phase == "request" {
				requests++
			}
			if ev.Phase == "response" {
				responses++
			}
		}
		Expect(requests).To(Equal(2))
		Expect(responses).To(Equal(2))
	})
	It("observes a repair routed to a newly constructed provider", func() {
		ai.RegisterProvider(ai.RuntimeOf(ai.OpenAI, ai.ModeAPI), func(ai.Config) (ai.Provider, error) {
			return &scriptedProvider{responses: []string{validJSON}}, nil
		})
		inner := &scriptedProvider{responses: []string{overCapJSON}}
		cfg := ai.Config{NoCache: true, APIKey: "example", SchemaRepair: api.SchemaRepairConfig{Model: api.Model{Name: "gpt-4o"}}}
		p, err := Wrap(inner, DefaultOptions(cfg, inner.GetRuntime())...)
		Expect(err).NotTo(HaveOccurred())
		var events []ProviderEvent
		release := ObserveProvider(func(ev ProviderEvent) { events = append(events, ev) })
		defer release()
		_, err = p.Execute(context.Background(), capRequest(api.SchemaStrictnessRetry))
		Expect(err).NotTo(HaveOccurred())
		Expect(events).To(HaveLen(4))
		Expect(events[2].Request.Prompt.User).To(ContainSubstring(overCapJSON))
	})
	It("does not allocate in unobserved wrapper calls", func() {
		input := make(chan ai.Event)
		close(input)
		p, err := Wrap(&observedStub{stubProvider: stubProvider{resp: &ai.Response{}}, events: input}, WithObservers())
		Expect(err).NotTo(HaveOccurred())
		ctx, req := context.Background(), ai.Request{}
		Expect(testing.AllocsPerRun(100, func() { _, _ = p.Execute(ctx, req) })).To(BeZero())
		streamer := p.(ai.StreamingProvider)
		Expect(testing.AllocsPerRun(100, func() { _, _ = streamer.ExecuteStream(ctx, req) })).To(BeZero())
	})
})
