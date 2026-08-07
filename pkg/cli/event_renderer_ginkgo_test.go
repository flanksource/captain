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
})
