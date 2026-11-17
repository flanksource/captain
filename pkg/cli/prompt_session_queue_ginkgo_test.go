package cli

import (
	"context"

	"github.com/flanksource/captain/pkg/ai"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("shared session admission", func() {
	It("joins an explicit resume and later grouped comments to the same conversation", func() {
		queue := sessionRunQueue{tails: map[string]*promptConversation{}}
		launch := promptLaunch{SessionKey: "shared", Rendered: PromptRenderResult{Provider: "anthropic", Mode: "agent"}}
		first, second, third := newRunStream(), newRunStream(), newRunStream()
		Expect(queue.enqueue(launch, first)).To(BeNil())
		queue.observeSession(first, launch.Rendered, ai.Event{SessionID: "provider-session"})
		resume := launch
		resume.SessionKey, resume.Rendered.Input.SessionID = "", "provider-session"
		Expect(queue.enqueue(resume, second)).To(BeIdenticalTo(first))
		Expect(queue.enqueue(launch, third)).To(BeIdenticalTo(second))
	})

	It("keeps the conversation when a queued turn is cancelled before it starts", func() {
		first, cancelled := newRunStream(), newRunStream()
		cancelled.previous = first
		cancelled.fail("stopped")
		first.complete(PromptRunSummary{Success: true, SessionID: "provider-session"})
		rendered := PromptRenderResult{}
		Expect(continuePromptSession(context.Background(), cancelled, &rendered)).To(Succeed())
		Expect(rendered.Input.SessionID).To(Equal("provider-session"))
		Expect(rendered.Config.SessionID).To(Equal("provider-session"))
	})

	It("does not resume a Captain admission identity when the provider never started", func() {
		first := newRunStream()
		first.setRun(PromptRunFrame{SessionID: "captain-admission"})
		first.fail("admission refused")
		rendered := PromptRenderResult{}
		Expect(continuePromptSession(context.Background(), first, &rendered)).To(MatchError("shared session did not start: admission refused"))
		Expect(rendered.Input.SessionID).To(BeEmpty())
	})

	It("cancels a queued turn without waiting for the active turn", func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		rendered := PromptRenderResult{}
		Expect(continuePromptSession(ctx, newRunStream(), &rendered)).To(MatchError(context.Canceled))
	})
})
