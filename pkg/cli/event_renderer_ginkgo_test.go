package cli

import (
	"bytes"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Captain event renderer", func() {
	It("buffers captured deltas and emits canonical transcript rows", func() {
		var output bytes.Buffer
		renderer := newEventRenderer(&output, false)

		renderer.Handle(0, ai.Event{Kind: ai.EventText, Text: "a keyed "})
		renderer.Handle(0, ai.Event{Kind: ai.EventText, Text: "HMAC"})
		Expect(output.String()).To(BeEmpty())

		renderer.Handle(0, ai.Event{
			Kind:       ai.EventToolUse,
			Tool:       "Bash",
			Input:      map[string]any{"command": `/bin/zsh -lc 'pnpm test'`},
			ToolCallID: "cmd-1",
			SessionID:  "thread-1",
			Model:      "gpt-5.6-sol",
		})
		renderer.Handle(0, ai.Event{
			Kind:       ai.EventToolResult,
			Tool:       "Bash",
			Text:       "captured command output",
			Success:    true,
			ToolCallID: "cmd-1",
		})
		renderer.Handle(0, ai.Event{Kind: ai.EventText, Text: "done"})
		renderer.Handle(0, ai.Event{Kind: ai.EventResult, Success: true})
		Expect(renderer.Flush()).To(Succeed())

		text := output.String()
		Expect(text).To(And(
			ContainSubstring("a keyed HMAC"),
			ContainSubstring("zsh"),
			ContainSubstring("pnpm test"),
			ContainSubstring("done"),
			Not(ContainSubstring("/bin/zsh -lc")),
			Not(ContainSubstring("captured command output")),
			Not(ContainSubstring("[tool-result]")),
		))
		Expect(strings.Count(text, "pnpm test")).To(Equal(1))
	})

	It("redraws an in-flight TTY message and finalizes it once", func() {
		var output bytes.Buffer
		renderer := newEventRenderer(&output, true)

		renderer.Handle(0, ai.Event{Kind: ai.EventText, Text: "hello "})
		renderer.Handle(0, ai.Event{Kind: ai.EventText, Text: "world"})
		Expect(renderer.Flush()).To(Succeed())

		Expect(output.String()).To(And(
			ContainSubstring("\r\x1b[2K"),
			ContainSubstring("hello world"),
			HaveSuffix("\n"),
		))
	})

	It("prints a hook notice on its own line, outside the model's prose", func() {
		var output bytes.Buffer
		renderer := newEventRenderer(&output, false)

		renderer.Handle(0, ai.Event{Kind: ai.EventText, Text: "applying the fix"})
		renderer.Handle(0, ai.Event{Kind: ai.EventSystem, Text: "[post-turn] committed abc1234: fix: the thing"})
		renderer.Handle(0, ai.Event{Kind: ai.EventResult, Success: true})
		Expect(renderer.Flush()).To(Succeed())

		text := output.String()
		Expect(text).To(ContainSubstring("[post-turn] committed abc1234"))
		// On its own line: a commit landing inside the sentence the model was
		// mid-way through writing is exactly what the buffering must prevent.
		Expect(text).NotTo(ContainSubstring("applying the fix[post-turn]"))
		Expect(strings.Count(text, "[post-turn] committed abc1234")).To(Equal(1))
	})

	It("reports an unmatched result without dumping its payload", func() {
		var output bytes.Buffer
		renderer := newEventRenderer(&output, false)

		renderer.Handle(0, ai.Event{
			Kind:       ai.EventToolResult,
			ToolCallID: "missing-call",
			Text:       "sensitive payload",
			Success:    false,
		})

		err := renderer.Flush()
		Expect(err).To(MatchError(ContainSubstring("missing-call")))
		Expect(output.String()).NotTo(ContainSubstring("sensitive payload"))
	})

	It("scopes tool-call identities to an iteration", func() {
		var output bytes.Buffer
		renderer := newEventRenderer(&output, false)

		for iteration, command := range []string{"first command", "second command"} {
			renderer.Handle(iteration, ai.Event{
				Kind:       ai.EventToolUse,
				Tool:       "Bash",
				Input:      map[string]any{"command": command},
				ToolCallID: "reused-call",
			})
		}

		Expect(renderer.Flush()).To(Succeed())
		Expect(output.String()).To(And(
			ContainSubstring("first command"),
			ContainSubstring("second command"),
		))
	})

	// A verdict is the loop's outcome, so it is rendered here rather than as the
	// one-line transcript row the accumulator would build for it: the row's fixed
	// preview budget would elide the very output the verdict exists to show.
	Describe("verify verdicts", func() {
		const failure = "fixtures/funeral-policy.yaml: policy Digital Funeral\n" +
			"    savePolicy rejected the policy: Please Enter a Valid Cover Amount"

		It("prints the verifier's output in full beneath the headline", func() {
			var output bytes.Buffer
			renderer := newEventRenderer(&output, false)

			renderer.Handle(0, ai.Event{Kind: ai.EventText, Text: "applying the fix"})
			renderer.Handle(0, ai.Event{
				Kind: ai.EventVerifyFailed, Tool: "verify:oipa-cli test fixtures/funeral-policy.yaml",
				Text: "failed in 5m7s: sh failed — verify:oipa-cli test fixtures/funeral-policy.yaml\n" + failure,
			})
			Expect(renderer.Flush()).To(Succeed())

			text := output.String()
			Expect(text).To(ContainSubstring("failed in 5m7s"))
			// Every line of the body survives: this is the failure the next turn
			// is about to be told about, and summarizing it here would leave the
			// reader with the same "why did it try again?" the verdict answers.
			for _, line := range strings.Split(failure, "\n") {
				Expect(text).To(ContainSubstring(line))
			}
			// It stands outside the model's prose, like a hook notice.
			Expect(text).NotTo(ContainSubstring("applying the fix✗"))
		})

		It("fits the headline to the output's width rather than a fixed budget", func() {
			var narrow, wide bytes.Buffer
			long := "failed in 5m7s: sh failed — verify:" + strings.Repeat("fixtures/a-long-path.yaml ", 12)

			for _, spec := range []struct {
				out   *bytes.Buffer
				width int
			}{{&narrow, 60}, {&wide, 200}} {
				renderer := newEventRenderer(spec.out, false)
				renderer.width = spec.width
				renderer.Handle(0, ai.Event{Kind: ai.EventVerifyFailed, Text: long})
				Expect(renderer.Flush()).To(Succeed())
			}

			Expect(len(narrow.String())).To(BeNumerically("<", len(wide.String())),
				"a wider terminal must show more of the line, not the same fixed cut")
			Expect(wide.String()).To(ContainSubstring("failed in 5m7s"))
		})

		// A fixture runner reports a snapshot every few hundred milliseconds. One
		// log line each turns the run's output into a column of superseded counts
		// that scrolls the model's own work off the screen, so the counts update
		// one line in place and only the verdict is committed to the scrollback.
		It("draws progress as one status line that updates in place", func() {
			var output bytes.Buffer
			renderer := newEventRenderer(&output, true)

			for done := 1; done <= 3; done++ {
				renderer.Handle(0, ai.Event{
					Kind: ai.EventVerifyProgress, Tool: "fixture", Raw: verifyReport(done, 5),
				})
			}
			renderer.Handle(0, ai.Event{
				Kind: ai.EventVerified, Success: true, Text: "passed in 4ms — fixture", Raw: verifyReport(5, 5),
			})
			Expect(renderer.Flush()).To(Succeed())

			text := output.String()
			Expect(text).To(ContainSubstring("3/5"))
			Expect(strings.Count(text, "1/5")).To(Equal(1), "each snapshot is drawn once, over the last one")
			Expect(strings.Count(text, "\n")).To(Equal(1),
				"only the verdict ends a line; three snapshots must not become three lines")
			Expect(text).To(ContainSubstring("passed in 4ms"))
		})

		It("keeps progress out of a redirected run's output entirely", func() {
			var output bytes.Buffer
			renderer := newEventRenderer(&output, false)

			renderer.Handle(0, ai.Event{Kind: ai.EventVerifyProgress, Tool: "fixture", Raw: verifyReport(1, 5)})
			Expect(renderer.Flush()).To(Succeed())

			// A file or CI log cannot redraw, so an in-place status line would
			// become exactly the per-snapshot log spam it exists to avoid.
			Expect(output.String()).To(BeEmpty())
		})

		It("renders a pass as a single line, since a passing check reports no output", func() {
			var output bytes.Buffer
			renderer := newEventRenderer(&output, false)

			renderer.Handle(0, ai.Event{
				Kind: ai.EventVerified, Success: true, Text: "passed in 4ms — verify:true",
			})
			Expect(renderer.Flush()).To(Succeed())

			Expect(output.String()).To(ContainSubstring("passed in 4ms"))
			Expect(strings.Count(strings.TrimRight(output.String(), "\n"), "\n")).To(Equal(0))
		})
	})
})
