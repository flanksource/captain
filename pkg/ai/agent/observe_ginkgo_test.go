package agent

import (
	"context"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/middleware"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/shell"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("runner observation", func() {
	It("publishes iteration and tool events with the loop iteration", func() {
		var events []middleware.ProviderEvent
		release := middleware.ObserveProvider(func(ev middleware.ProviderEvent) { events = append(events, ev) })
		defer release()
		r := &Runner[any]{Provider: &fakeProvider{}}
		hc := &HookContext{Response: &ai.Response{}, Iteration: 2}
		r.recordEvent(hc, ai.Event{Kind: ai.EventTurnStart})
		r.recordEvent(hc, ai.Event{Kind: ai.EventToolUse, Tool: "Read"})
		Expect(events).To(HaveLen(2))
		Expect(events[0].Phase).To(Equal("iteration"))
		Expect(events[1].Phase).To(Equal("tool"))
		Expect(events[1].Iter).To(Equal(2))
		Expect(events[1].Event.Tool).To(Equal("Read"))
	})
	It("publishes events from a running turn", func() {
		var events []middleware.ProviderEvent
		release := middleware.ObserveProvider(func(ev middleware.ProviderEvent) { events = append(events, ev) })
		defer release()
		provider := &fakeProvider{events: func(int) []ai.Event {
			return []ai.Event{{Kind: ai.EventToolUse, Tool: "Read"}, {Kind: ai.EventResult, Success: true}}
		}}
		r := &Runner[any]{Provider: provider, Request: ai.Request{Prompt: api.Prompt{User: "inspect"}}}
		_, err := r.Run(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(events).To(HaveLen(2))
		Expect(events[0].Phase).To(Equal("iteration"))
		Expect(events[1].Phase).To(Equal("tool"))
		Expect(events[1].Event.Tool).To(Equal("Read"))
	})
	It("redacts environment values in the iteration's embedded request", func() {
		var events []middleware.ProviderEvent
		release := middleware.ObserveProvider(func(ev middleware.ProviderEvent) { events = append(events, ev) })
		defer release()
		r := &Runner[any]{Provider: &fakeProvider{}}
		hc := &HookContext{Response: &ai.Response{}}
		request := ai.Request{Setup: &shell.Setup{Env: []string{"TOKEN=secret"}}}
		r.recordEvent(hc, ai.Event{Kind: ai.EventTurnStart, Raw: &ai.TurnStart{Request: request}})
		Expect(events).To(HaveLen(1))
		turn := events[0].Event.Raw.(*ai.TurnStart)
		Expect(turn.Request.Setup.Env).To(Equal([]string{"TOKEN=[REDACTED]"}))
		Expect(request.Setup.Env).To(Equal([]string{"TOKEN=secret"}))
	})
})
