package cli

import (
	"bytes"
	"io"
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

	Describe("Writer", func() {
		const (
			streamed = "drafting the fix"
			logLine  = "warning: model is not recognized"
		)

		It("commits a log line above the in-place turn and redraws the turn beneath it", func() {
			var output bytes.Buffer
			renderer := newEventRenderer(&output, true)
			renderer.Handle(0, ai.Event{Kind: ai.EventText, Text: streamed})
			output.Reset()

			_, err := io.WriteString(renderer.Writer(), logLine+"\n")
			Expect(err).NotTo(HaveOccurred())

			written := output.String()
			Expect(written).To(HavePrefix("\r\x1b[2K" + logLine + "\n"))
			Expect(strings.TrimPrefix(written, "\r\x1b[2K"+logLine+"\n")).To(ContainSubstring(streamed))
		})

		It("holds log lines with events while suspended and replays both in arrival order", func() {
			const before, after = "event before the log", "event after the log"
			var output bytes.Buffer
			renderer := newEventRenderer(&output, false)

			// Each notice is closed by a result, which commits it: an open turn
			// is still pending, and lines written meanwhile belong above it.
			resume := renderer.Suspend()
			renderer.Handle(0, ai.Event{Kind: ai.EventSystem, Text: before})
			renderer.Handle(0, ai.Event{Kind: ai.EventResult, Success: true})
			_, err := io.WriteString(renderer.Writer(), logLine+"\n")
			Expect(err).NotTo(HaveOccurred())
			renderer.Handle(0, ai.Event{Kind: ai.EventSystem, Text: after})
			renderer.Handle(0, ai.Event{Kind: ai.EventResult, Success: true})
			Expect(output.String()).To(BeEmpty())

			resume()
			Expect(renderer.Flush()).To(Succeed())
			written := output.String()
			Expect(strings.Index(written, before)).To(BeNumerically("<", strings.Index(written, logLine)))
			Expect(strings.Index(written, logLine)).To(BeNumerically("<", strings.Index(written, after)))
		})

		It("commits a partial line only once its newline arrives, or on Flush", func() {
			var output bytes.Buffer
			renderer := newEventRenderer(&output, false)
			writer := renderer.Writer()

			_, _ = io.WriteString(writer, "npm install ")
			Expect(output.String()).To(BeEmpty())
			_, _ = io.WriteString(writer, "done\nadded 3 packages")
			Expect(output.String()).To(Equal("npm install done\n"))

			Expect(renderer.Flush()).To(Succeed())
			Expect(output.String()).To(Equal("npm install done\nadded 3 packages\n"))
		})
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
