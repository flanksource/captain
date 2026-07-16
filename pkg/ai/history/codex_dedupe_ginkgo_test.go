package history

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func dedupeUses(lines ...string) []ToolUse {
	uses, err := ExtractCodexToolUsesFromReader(strings.NewReader(strings.Join(lines, "\n")))
	Expect(err).ToNot(HaveOccurred())
	return uses
}

func toolNames(uses []ToolUse) []string {
	out := make([]string, 0, len(uses))
	for _, use := range uses {
		out = append(out, use.Tool)
	}
	return out
}

const dedupeSessionMeta = `{"timestamp":"2026-01-21T12:43:41.000Z","type":"session_meta","payload":{"id":"sess-dedupe","cwd":"/repo"}}`

// Codex logs every message twice -- once as an event_msg, once as a
// response_item -- so a row must merge with its twin but never with a genuine
// repeat of the same text.
var _ = Describe("codex twin dedupe", func() {
	It("merges a skewed assistant twin, keeping the response_item content", func() {
		uses := dedupeUses(
			dedupeSessionMeta,
			`{"timestamp":"2026-01-21T12:43:41.393Z","type":"event_msg","payload":{"type":"agent_message","message":"done"}}`,
			`{"timestamp":"2026-01-21T12:43:41.396Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}}`,
		)

		Expect(toolNames(uses)).To(Equal([]string{"Assistant"}))
		Expect(uses[0].RecordType).To(Equal("response_item.message"))
	})

	It("merges a response_item-first user twin, keeping the first row", func() {
		uses := dedupeUses(
			dedupeSessionMeta,
			`{"timestamp":"2026-01-21T12:43:41.393Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}`,
			`{"timestamp":"2026-01-21T12:43:41.394Z","type":"event_msg","payload":{"type":"user_message","message":"hi"}}`,
		)

		Expect(toolNames(uses)).To(Equal([]string{"User"}))
		Expect(uses[0].RecordType).To(Equal("response_item.message"))
	})

	// The family set, not adjacency, is what saves this: with no intervening
	// record the two messages are back to back, so a rule keyed on "the previous
	// row came from a different family" would fold all four records into one.
	It("keeps a repeated message logged as two full twin pairs back to back", func() {
		uses := dedupeUses(
			dedupeSessionMeta,
			`{"timestamp":"2026-01-21T12:43:41.393Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}}`,
			`{"timestamp":"2026-01-21T12:43:41.394Z","type":"event_msg","payload":{"type":"user_message","message":"continue"}}`,
			`{"timestamp":"2026-01-22T08:02:23.240Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}}`,
			`{"timestamp":"2026-01-22T08:02:23.241Z","type":"event_msg","payload":{"type":"user_message","message":"continue"}}`,
		)

		Expect(toolNames(uses)).To(Equal([]string{"User", "User"}))
		Expect(uses[0].Timestamp.UTC().Format(nanoLayout)).To(Equal("2026-01-21T12:43:41.393Z"))
		Expect(uses[1].Timestamp.UTC().Format(nanoLayout)).To(Equal("2026-01-22T08:02:23.24Z"))
	})

	// Drawn from rollout-2026-01-21T12-32-48-019be01d: the same prompt re-sent
	// 19 hours later. Twin skew is unbounded, so no time window may be used to
	// tell these apart -- only the intervening record and the family set.
	It("keeps a prompt re-sent after an aborted turn", func() {
		uses := dedupeUses(
			dedupeSessionMeta,
			`{"timestamp":"2026-01-21T12:43:41.393Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Group the transactions"}]}}`,
			`{"timestamp":"2026-01-21T12:43:41.393Z","type":"event_msg","payload":{"type":"user_message","message":"Group the transactions"}}`,
			`{"timestamp":"2026-01-21T12:43:44.797Z","type":"event_msg","payload":{"type":"turn_aborted","turn_id":"turn-1"}}`,
			`{"timestamp":"2026-01-22T08:02:23.240Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Group the transactions"}]}}`,
			`{"timestamp":"2026-01-22T08:02:23.241Z","type":"event_msg","payload":{"type":"user_message","message":"Group the transactions"}}`,
		)

		Expect(toolNames(uses)).To(Equal([]string{"User", "TurnAborted", "User"}))
	})

	It("keeps an identical assistant verdict repeated across turns", func() {
		uses := dedupeUses(
			dedupeSessionMeta,
			`{"timestamp":"2026-01-21T12:43:41.393Z","type":"event_msg","payload":{"type":"agent_message","message":"{\"outcome\":\"allow\"}"}}`,
			`{"timestamp":"2026-01-21T12:43:41.394Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{\"outcome\":\"allow\"}"}]}}`,
			`{"timestamp":"2026-01-21T12:44:05.078Z","type":"event_msg","payload":{"type":"agent_message","message":"{\"outcome\":\"allow\"}"}}`,
			`{"timestamp":"2026-01-21T12:44:05.079Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{\"outcome\":\"allow\"}"}]}}`,
			`{"timestamp":"2026-01-21T12:44:18.601Z","type":"event_msg","payload":{"type":"agent_message","message":"{\"outcome\":\"allow\"}"}}`,
			`{"timestamp":"2026-01-21T12:44:18.602Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{\"outcome\":\"allow\"}"}]}}`,
		)

		Expect(toolNames(uses)).To(Equal([]string{"Assistant", "Assistant", "Assistant"}))
	})

	It("merges a skewed plaintext reasoning twin", func() {
		uses := dedupeUses(
			dedupeSessionMeta,
			`{"timestamp":"2026-01-21T12:43:41.393Z","type":"event_msg","payload":{"type":"agent_reasoning","text":"Checking the parser"}}`,
			`{"timestamp":"2026-01-21T12:43:41.396Z","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"Checking the parser"}]}}`,
		)

		Expect(toolNames(uses)).To(Equal([]string{"Reasoning"}))
		Expect(uses[0].RecordType).To(Equal("response_item.reasoning"))
	})

	It("does not merge identical messages separated by other chat content", func() {
		uses := dedupeUses(
			dedupeSessionMeta,
			`{"timestamp":"2026-01-21T12:43:41.393Z","type":"event_msg","payload":{"type":"user_message","message":"<turn_aborted>"}}`,
			`{"timestamp":"2026-01-21T12:43:42.000Z","type":"event_msg","payload":{"type":"agent_message","message":"working on it"}}`,
			`{"timestamp":"2026-01-21T12:43:43.000Z","type":"event_msg","payload":{"type":"user_message","message":"<turn_aborted>"}}`,
		)

		Expect(toolNames(uses)).To(Equal([]string{"User", "Assistant", "User"}))
	})
})
