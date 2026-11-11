package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/observe"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/connection"
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
		var events []observe.ProviderEvent
		release := observe.ObserveProvider(func(ev observe.ProviderEvent) { events = append(events, ev) })
		defer release()
		_, err = p.Execute(context.Background(), ai.Request{})
		Expect(err).To(MatchError(failure))
		Expect(events).To(HaveLen(2))
		Expect(events[0].Phase).To(Equal(observe.PhaseRequest))
		Expect(events[1].Phase).To(Equal(observe.PhaseResponse))
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
		observed := make(chan observe.ProviderEvent, 2)
		release := observe.ObserveProvider(func(ev observe.ProviderEvent) { observed <- ev })
		defer release()
		stream, err := p.(ai.StreamingProvider).ExecuteStream(context.Background(), ai.Request{})
		Expect(err).NotTo(HaveOccurred())
		Expect((<-observed).Phase).To(Equal(observe.PhaseRequest))
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
		release := observe.ObserveProvider(func(observe.ProviderEvent) { panic("observer failure") })
		Expect(func() { observe.PublishProvider(observe.ProviderEvent{}) }).NotTo(Panic())
		release()
		Expect(testing.AllocsPerRun(100, func() { observe.PublishProvider(observe.ProviderEvent{}) })).To(BeZero())
	})
	It("reports a stream startup failure", func() {
		failure := errors.New("stream unavailable")
		p, err := Wrap(&observedStub{err: failure}, WithObservers())
		Expect(err).NotTo(HaveOccurred())
		var events []observe.ProviderEvent
		release := observe.ObserveProvider(func(ev observe.ProviderEvent) { events = append(events, ev) })
		defer release()
		_, err = p.(ai.StreamingProvider).ExecuteStream(context.Background(), ai.Request{})
		Expect(err).To(MatchError(failure))
		Expect(events).To(HaveLen(2))
		Expect(events[1].Err).To(MatchError(failure))
	})
	It("includes observation in the default stack", func() {
		p, err := Wrap(stubProvider{resp: &ai.Response{}}, DefaultOptions(ai.Config{NoCache: true}, ai.RuntimeOf(ai.Anthropic, ai.ModeAPI))...)
		Expect(err).NotTo(HaveOccurred())
		var events []observe.ProviderEvent
		release := observe.ObserveProvider(func(ev observe.ProviderEvent) { events = append(events, ev) })
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
		observed := make(chan observe.ProviderEvent, 4)
		release := observe.ObserveProvider(func(ev observe.ProviderEvent) { observed <- ev })
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
			if ev.Phase != observe.PhaseRequest {
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
	It("isolates the observed buffered response from the caller's response", func() {
		response := &ai.Response{Text: "original", Context: &api.ContextUsage{UsedTokens: 5}, Usage: ai.Usage{InputTokens: 7}, CostUSD: 0.1}
		p, err := Wrap(&observedStub{stubProvider: stubProvider{resp: response}}, WithObservers())
		Expect(err).NotTo(HaveOccurred())
		var observed observe.ProviderEvent
		release := observe.ObserveProvider(func(ev observe.ProviderEvent) {
			if ev.Phase == observe.PhaseResponse {
				observed = ev
				ev.Response.Text = "changed"
				ev.Response.Context.UsedTokens = 9
			}
		})
		defer release()
		got, err := p.Execute(context.Background(), ai.Request{})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(BeIdenticalTo(response))
		Expect(got.Text).To(Equal("original"))
		Expect(got.Context.UsedTokens).To(Equal(5))
		Expect(observed.Usage.InputTokens).To(Equal(7))
		Expect(observed.Cost).To(Equal(0.1))
	})
	It("isolates the observed streamed response from the forwarded result event", func() {
		input := make(chan ai.Event, 1)
		input <- ai.Event{Kind: ai.EventResult, Success: true, Context: &api.ContextUsage{UsedTokens: 5}}
		close(input)
		p, err := Wrap(&observedStub{events: input}, WithObservers())
		Expect(err).NotTo(HaveOccurred())
		observed := make(chan observe.ProviderEvent, 1)
		release := observe.ObserveProvider(func(ev observe.ProviderEvent) {
			if ev.Phase == observe.PhaseResponse {
				ev.Response.Context.UsedTokens = 9
				observed <- ev
			}
		})
		defer release()
		stream, err := p.(ai.StreamingProvider).ExecuteStream(context.Background(), ai.Request{})
		Expect(err).NotTo(HaveOccurred())
		var forwarded ai.Event
		for ev := range stream {
			forwarded = ev
		}
		result := <-observed
		Expect(result.Response.Context.UsedTokens).To(Equal(9))
		Expect(forwarded.Context.UsedTokens).To(Equal(5))
	})
	It("redacts connection credentials in observed requests without mutating the original request", func() {
		const secret = "connection-secret"
		secretRef := func() *types.EnvVar {
			return &types.EnvVar{ValueStatic: secret, ValueFrom: &types.EnvVarSource{SecretKeyRef: &types.SecretKeySelector{Key: "k"}}}
		}
		aws := connection.AWSConnection{AccessKey: *secretRef(), SecretKey: *secretRef(), SessionToken: *secretRef(), Region: "us-east-1"}
		gcp := connection.GCPConnection{Credentials: secretRef(), Project: "project-x"}
		req := ai.Request{Setup: &shell.Setup{Connections: connection.ExecConnections{
			AWS:   &aws,
			GCP:   &gcp,
			Azure: &connection.AzureConnection{ClientID: secretRef(), ClientSecret: secretRef(), TenantID: "tenant-x"},
			Kubernetes: &connection.KubernetesConnection{
				KubeconfigConnection: connection.KubeconfigConnection{Kubeconfig: secretRef()},
				EKS:                  &connection.EKSConnection{AWSConnection: aws},
				GKE:                  &connection.GKEConnection{GCPConnection: gcp},
				CNRM:                 &connection.CNRMConnection{GKE: connection.GKEConnection{GCPConnection: gcp}},
			},
		}}}
		p, err := Wrap(&observedStub{stubProvider: stubProvider{resp: &ai.Response{}}}, WithObservers())
		Expect(err).NotTo(HaveOccurred())
		var redacted *ai.Request
		release := observe.ObserveProvider(func(ev observe.ProviderEvent) {
			if ev.Phase == observe.PhaseRequest {
				redacted = ev.Request
			}
		})
		defer release()
		_, err = p.Execute(context.Background(), req)
		Expect(err).NotTo(HaveOccurred())
		Expect(redacted).NotTo(BeNil())
		Expect(redacted.Setup.Connections.AWS.Region).To(Equal("us-east-1"))
		Expect(redacted.Setup.Connections.Azure.TenantID).To(Equal("tenant-x"))
		Expect(fmt.Sprintf("%+v", redacted.Setup.Connections)).NotTo(ContainSubstring(secret))
		raw, err := json.Marshal(redacted.Setup.Connections)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(raw)).NotTo(ContainSubstring(secret))
		Expect(string(raw)).NotTo(ContainSubstring("valueFrom"))
		Expect(req.Setup.Connections.Azure.ClientSecret.ValueStatic).To(Equal(secret))
		Expect(req.Setup.Connections.Kubernetes.CNRM.GKE.Credentials.ValueStatic).To(Equal(secret))
		Expect(req.Setup.Connections.AWS.SecretKey.ValueFrom).NotTo(BeNil())
	})
	It("drains a cancelled stream and forwards a waiting terminal result", func() {
		input := make(chan ai.Event)
		producerDone := make(chan struct{})
		p, err := Wrap(&observedStub{events: input}, WithObservers())
		Expect(err).NotTo(HaveOccurred())
		release := observe.ObserveProvider(func(observe.ProviderEvent) {})
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
		release := observe.ObserveProvider(func(observe.ProviderEvent) {})
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
		release := observe.ObserveProvider(func(observe.ProviderEvent) {})
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
		first := make(chan observe.ProviderEvent, 2)
		release := observe.ObserveProvider(func(ev observe.ProviderEvent) { first <- ev })
		defer release()
		stream, err := p.(ai.StreamingProvider).ExecuteStream(context.Background(), ai.Request{})
		Expect(err).NotTo(HaveOccurred())
		<-first
		late := make(chan observe.ProviderEvent, 1)
		lateRelease := observe.ObserveProvider(func(ev observe.ProviderEvent) { late <- ev })
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
		release := observe.ObserveProvider(func(ev observe.ProviderEvent) {
			if ev.Phase == observe.PhaseResponse {
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
		var events []observe.ProviderEvent
		release := observe.ObserveProvider(func(ev observe.ProviderEvent) { events = append(events, ev) })
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
		var events []observe.ProviderEvent
		release := observe.ObserveProvider(func(ev observe.ProviderEvent) { events = append(events, ev) })
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
		observed := make(chan observe.ProviderEvent, 4)
		release := observe.ObserveProvider(func(ev observe.ProviderEvent) { observed <- ev })
		defer release()
		stream, err := p.(ai.StreamingProvider).ExecuteStream(context.Background(), capRequest(api.SchemaStrictnessRetry))
		Expect(err).NotTo(HaveOccurred())
		for range stream {
		}
		var requests, responses int
		for i := 0; i < 4; i++ {
			var ev observe.ProviderEvent
			Eventually(observed, time.Second).Should(Receive(&ev))
			if ev.Phase == observe.PhaseRequest {
				requests++
			}
			if ev.Phase == observe.PhaseResponse {
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
		var events []observe.ProviderEvent
		release := observe.ObserveProvider(func(ev observe.ProviderEvent) { events = append(events, ev) })
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
