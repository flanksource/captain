package aichat

import (
	"context"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Persisted assistant context", func() {
	It("updates thread context without billing usage and clears missing telemetry", func() {
		ctx := context.Background()
		store := NewMemoryThreadStore()
		thread, err := store.Create(ctx, "Example context")
		Expect(err).NotTo(HaveOccurred())
		service := NewService(ServiceOptions{Threads: FixedThreadStore(store)})
		snapshot := &api.ContextUsage{WindowTokens: 258400, FreePercent: 100}
		Expect(service.persistEvent(ctx, thread.ID, api.Event{Kind: api.EventResult, Context: snapshot}, api.Model{}, nil)).To(Succeed())
		thread, err = store.Get(ctx, thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(thread.Context).To(Equal(snapshot))
		Expect(service.persistEvent(ctx, thread.ID, api.Event{Kind: api.EventResult}, api.Model{}, nil)).To(Succeed())
		thread, err = store.Get(ctx, thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(thread.Context).To(BeNil())
	})

	It("retains a native context snapshot even without billing usage", func() {
		builder, err := newAssistantMessageBuilder(assistantMessageBuilderOptions{MessageID: "assistant-1"})
		Expect(err).NotTo(HaveOccurred())
		context := &api.ContextUsage{UsedTokens: 89595, WindowTokens: 258400, FreePercent: 69}
		Expect(builder.apply(api.Event{Kind: api.EventResult, Success: true, Context: context})).To(Succeed())
		Expect(builder.message.Metadata.Context).To(Equal(context))
	})
})
