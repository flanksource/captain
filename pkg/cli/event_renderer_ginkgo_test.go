package cli

import (
	"io"
	"os"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/claude"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Captain event renderer", func() {
	It("keeps text deltas contiguous and renders a command once", func() {
		reader, writer, err := os.Pipe()
		Expect(err).NotTo(HaveOccurred())

		render := NewEventRenderer(writer)
		for _, delta := range []string{"a", " keyed", " H", "MAC", " so", " the", " token"} {
			render(0, ai.Event{Kind: ai.EventText, Text: delta, Model: "gpt-5.6-sol"})
		}
		render(0, ai.Event{
			Kind:       ai.EventToolUse,
			Tool:       "Bash",
			Input:      map[string]any{"command": "pwd"},
			ToolCallID: "cmd-1",
			SessionID:  "thread-1",
			Model:      "gpt-5.6-sol",
			Raw: claude.ToolUse{
				Tool:      "Bash",
				Input:     map[string]any{"command": "pwd"},
				ToolUseID: "cmd-1",
				SessionID: "thread-1",
				Source:    "codex",
				Model:     "gpt-5.6-sol",
			},
		})

		Expect(writer.Close()).To(Succeed())
		output, err := io.ReadAll(reader)
		Expect(err).NotTo(HaveOccurred())
		Expect(reader.Close()).To(Succeed())

		text := string(output)
		Expect(text).To(ContainSubstring("a keyed HMAC so the token"))
		Expect(strings.Count(text, "pwd")).To(Equal(1))
		Expect(text).NotTo(ContainSubstring("[gpt-5.6-sol]"))
	})
})
