package session

import (
	"strings"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = ginkgo.Describe("AI SDK chat projection", func() {
	ginkgo.It("drops synthetic transcript tools while preserving terminal tool calls", func() {
		entries, err := claude.ReadHistory(strings.NewReader(`{"type":"ai-title","aiTitle":"List pending approval requests","sessionId":"provider-session"}
{"type":"assistant","uuid":"assistant-tool","sessionId":"provider-session","message":{"role":"assistant","content":[{"type":"tool_use","id":"call-list","name":"Bash","input":{"command":"pwd"}}]}}
{"type":"user","uuid":"tool-result","sessionId":"provider-session","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-list","content":"/work/example"}]}}`))
		Expect(err).NotTo(HaveOccurred())

		recovered := buildSession(claude.ParsedSession{
			SessionID: "provider-session",
			Transcripts: []claude.ParsedTranscript{{
				Path: "/work/provider-session.jsonl", Entries: entries,
			}},
		})
		Expect(recovered.Messages[0].Parts[0].ToolCallID).To(Equal("ai-title/"))
		Expect(recovered.Messages[0].Parts[0].State).To(Equal(ToolStateInputAvailable))

		messages, _ := recovered.ToUIMessages()

		Expect(messages).To(HaveLen(1))
		Expect(messages[0].Parts).To(HaveLen(1))
		Expect(messages[0].Parts[0].ToolName).To(Equal("Bash"))
		Expect(messages[0].Parts[0].State).To(Equal(ToolStateOutputAvailable))
		Expect(string(messages[0].Parts[0].Output)).To(Equal(`"/work/example"`))
	})
})
