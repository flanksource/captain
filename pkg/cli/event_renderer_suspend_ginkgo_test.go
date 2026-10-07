package cli

import (
	"bytes"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Event renderer Suspend", func() {
	const heldText = "written while the approval form was open"

	It("holds events while suspended and writes them in order on resume", func() {
		var output bytes.Buffer
		renderer := newEventRenderer(&output, false)
		renderer.Handle(0, ai.Event{Kind: ai.EventSystem, Text: "before the prompt"})
		before := output.String()

		resume := renderer.Suspend()
		renderer.Handle(0, ai.Event{Kind: ai.EventText, Text: heldText})
		renderer.Handle(0, ai.Event{Kind: ai.EventResult, Success: true})
		Expect(output.String()).To(Equal(before))

		resume()
		Expect(renderer.Flush()).To(Succeed())
		Expect(strings.TrimPrefix(output.String(), before)).To(ContainSubstring(heldText))
	})

	It("erases the in-place line on suspend and redraws it on resume", func() {
		var output bytes.Buffer
		renderer := newEventRenderer(&output, true)
		renderer.Handle(0, ai.Event{Kind: ai.EventText, Text: "drafting the fix"})
		Expect(output.String()).To(ContainSubstring("drafting the fix"))

		output.Reset()
		resume := renderer.Suspend()
		Expect(output.String()).To(Equal("\r\x1b[2K"))

		output.Reset()
		renderer.Handle(0, ai.Event{Kind: ai.EventText, Text: " and more"})
		Expect(output.String()).To(BeEmpty())

		resume()
		Expect(output.String()).To(ContainSubstring("drafting the fix and more"))
		Expect(renderer.Flush()).To(Succeed())
		Expect(output.String()).To(HaveSuffix("\n"))
	})

	It("rewrites an erased turn that a held event finishes", func() {
		var output bytes.Buffer
		renderer := newEventRenderer(&output, true)
		renderer.Handle(0, ai.Event{Kind: ai.EventText, Text: "drafting the fix"})

		resume := renderer.Suspend()
		renderer.Handle(0, ai.Event{Kind: ai.EventResult, Success: true})
		output.Reset()
		resume()
		Expect(renderer.Flush()).To(Succeed())
		Expect(output.String()).To(And(ContainSubstring("drafting the fix"), HaveSuffix("\n")))
	})

	It("refuses to flush while suspended", func() {
		renderer := newEventRenderer(&bytes.Buffer{}, false)
		resume := renderer.Suspend()
		renderer.Handle(0, ai.Event{Kind: ai.EventText, Text: heldText})

		Expect(renderer.Flush()).To(MatchError(ContainSubstring("suspended")))
		resume()
		Expect(renderer.Flush()).To(Succeed())
	})

	It("treats a second resume as a no-op and stays held until every suspend resumes", func() {
		var output bytes.Buffer
		renderer := newEventRenderer(&output, false)
		outer := renderer.Suspend()
		inner := renderer.Suspend()
		renderer.Handle(0, ai.Event{Kind: ai.EventSystem, Text: heldText})

		inner()
		inner()
		Expect(renderer.Flush()).To(MatchError(ContainSubstring("suspended")))
		Expect(output.String()).To(BeEmpty())
		outer()
		Expect(renderer.Flush()).To(Succeed())
		Expect(output.String()).To(ContainSubstring(heldText))
	})

	It("is safe to suspend from another goroutine while events stream", func() {
		const events = 200
		output := &lockedBuffer{}
		renderer := newEventRenderer(output, true)

		done := make(chan struct{})
		go func() {
			defer GinkgoRecover()
			defer close(done)
			for i := range events {
				renderer.Handle(i, ai.Event{Kind: ai.EventSystem, Text: "event"})
			}
		}()
		for range 20 {
			renderer.Suspend()()
		}
		<-done
		Expect(renderer.Flush()).To(Succeed())
		Expect(strings.Count(output.String(), "event")).To(Equal(events))
	})
})
