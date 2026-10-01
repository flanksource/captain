package history

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Codex user lines the person did not type", func() {
	const abortNotice = `"<turn_aborted>\nThe user interrupted the previous turn on purpose.\n</turn_aborted>"`

	DescribeTable("an interrupt notice is a System row, not the person's reply",
		func(line string) {
			uses := dedupeUses(dedupeSessionMeta, line)

			Expect(toolNames(uses)).To(Equal([]string{"System"}))
		},
		Entry("as a response item", `{"timestamp":"2026-01-21T12:43:41.393Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":`+abortNotice+`}]}}`),
		Entry("as an event message", `{"timestamp":"2026-01-21T12:43:41.393Z","type":"event_msg","payload":{"type":"user_message","message":`+abortNotice+`}}`),
	)

	It("keeps a typed message that mentions the notice as the person's", func() {
		uses := dedupeUses(dedupeSessionMeta,
			`{"timestamp":"2026-01-21T12:43:41.393Z","type":"event_msg","payload":{"type":"user_message","message":"why did I see <turn_aborted> earlier?"}}`)

		Expect(toolNames(uses)).To(Equal([]string{"User"}))
	})
})
