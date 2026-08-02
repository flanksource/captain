package session

import (
	"github.com/flanksource/captain/pkg/claude/tools"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = ginkgo.Describe("canonical transcript rows", func() {
	ginkgo.It("uses the same Clicky row for session messages and standalone history tools", func() {
		input := map[string]any{"command": "pnpm test", "shell": "zsh"}
		session := &Session{Messages: []Message{{
			Role: "assistant",
			Parts: []Part{{
				Type:     PartTool,
				ToolName: "Bash",
				Input:    marshalInput(input),
			}},
		}}}

		sessionRows := session.TranscriptRows()
		historyRow := NewTranscriptRow(tools.NewTool(tools.BaseTool{RawTool: "Bash", Input: input}))

		Expect(sessionRows).To(HaveLen(1))
		Expect(sessionRows[0].Pretty().String()).To(Equal(historyRow.Pretty().String()))
		Expect(TranscriptList(sessionRows).String()).To(ContainSubstring("pnpm test"))
	})
})
