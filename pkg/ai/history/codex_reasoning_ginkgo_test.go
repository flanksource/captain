package history

import (
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const nanoLayout = time.RFC3339Nano

// reasoningUses returns only the Reasoning rows produced from a rollout stream.
func reasoningUses(lines ...string) []ToolUse {
	uses, err := ExtractCodexToolUsesFromReader(strings.NewReader(strings.Join(lines, "\n")))
	Expect(err).ToNot(HaveOccurred())
	var out []ToolUse
	for _, use := range uses {
		if use.Tool == "Reasoning" {
			out = append(out, use)
		}
	}
	return out
}

const (
	reasoningSessionMeta = `{"timestamp":"2026-07-16T11:14:45.000Z","type":"session_meta","payload":{"id":"sess-enc","cwd":"/repo","cli_version":"0.160.0"}}`
	reasoningTurnContext = `{"timestamp":"2026-07-16T11:14:55.426Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.5","effort":"high"}}`
)

var _ = Describe("codex reasoning collapse", func() {
	// Modern Codex ships reasoning as summary:[] + encrypted_content. The text is
	// unrecoverable, but the record still proves the session was alive at that
	// instant, so its timestamp must reach extendRange via a collapsed row.
	It("collapses a contiguous run of contentless reasoning onto its last timestamp", func() {
		uses := reasoningUses(
			reasoningSessionMeta,
			reasoningTurnContext,
			`{"timestamp":"2026-07-16T11:15:04.024Z","type":"response_item","payload":{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"gAAAA1","internal_chat_message_metadata_passthrough":{"turn_id":"turn-1"}}}`,
			`{"timestamp":"2026-07-16T11:32:58.744Z","type":"response_item","payload":{"type":"reasoning","id":"rs_2","summary":[],"encrypted_content":"gAAAA2","internal_chat_message_metadata_passthrough":{"turn_id":"turn-1"}}}`,
		)

		Expect(uses).To(HaveLen(1))
		use := uses[0]
		Expect(use.Timestamp.UTC().Format(nanoLayout)).To(Equal("2026-07-16T11:32:58.744Z"))
		Expect(use.Input["first_at"]).To(Equal("2026-07-16T11:15:04.024Z"))
		Expect(use.Input["last_at"]).To(Equal("2026-07-16T11:32:58.744Z"))
		Expect(use.Input["count"]).To(Equal(2))
		Expect(use.Input["text"]).To(ContainSubstring("2 encrypted reasoning records"))
		Expect(use.TurnID).To(Equal("turn-1"))
		Expect(use.Model).To(Equal("gpt-5.5"))
		Expect(use.ReasoningEffort).To(Equal("high"))
		Expect(use.SessionID).To(Equal("sess-enc"))
		Expect(use.CWD).To(Equal("/repo"))
		Expect(use.Source).To(Equal("codex"))
		Expect(use.RecordType).To(Equal("response_item.reasoning"))
		// The span is keyed on the line of its FIRST record, which is what makes a
		// re-parse of a grown transcript reproduce this row rather than append a
		// longer one beside it.
		Expect(use.SourceLine).To(Equal(int64(3)))
	})

	// A span must be final when emitted, or a tailing re-parse emits a longer span
	// with a different count and shifts every downstream ordinal. Finality means
	// "ends at the next non-reasoning record", not "ends at the turn boundary":
	// the tool call below splits one turn into two spans.
	It("ends a span at the first non-reasoning record rather than at the turn boundary", func() {
		uses := reasoningUses(
			reasoningSessionMeta,
			reasoningTurnContext,
			`{"timestamp":"2026-07-16T11:15:04.024Z","type":"response_item","payload":{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"gAAAA1","internal_chat_message_metadata_passthrough":{"turn_id":"turn-1"}}}`,
			`{"timestamp":"2026-07-16T11:20:00.000Z","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":\"ls\"}","call_id":"call-1"}}`,
			`{"timestamp":"2026-07-16T11:20:01.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"ok"}}`,
			`{"timestamp":"2026-07-16T11:32:58.744Z","type":"response_item","payload":{"type":"reasoning","id":"rs_2","summary":[],"encrypted_content":"gAAAA2","internal_chat_message_metadata_passthrough":{"turn_id":"turn-1"}}}`,
		)

		Expect(uses).To(HaveLen(2))
		Expect(uses[0].Input["count"]).To(Equal(1))
		Expect(uses[0].Timestamp.UTC().Format(nanoLayout)).To(Equal("2026-07-16T11:15:04.024Z"))
		Expect(uses[0].SourceLine).To(Equal(int64(3)))
		Expect(uses[0].Provisional).To(BeFalse())

		Expect(uses[1].Input["count"]).To(Equal(1))
		Expect(uses[1].Timestamp.UTC().Format(nanoLayout)).To(Equal("2026-07-16T11:32:58.744Z"))
		Expect(uses[1].SourceLine).To(Equal(int64(6)))
		// Only the trailing span is provisional: the next append can extend it, and
		// the stable line key is what lets that append update this row in place.
		Expect(uses[1].Provisional).To(BeTrue())
	})

	// Older rollouts carry real plaintext summaries; those must keep rendering
	// one row each rather than being folded into a span.
	It("keeps one row per plaintext summary", func() {
		uses := reasoningUses(
			reasoningSessionMeta,
			reasoningTurnContext,
			`{"timestamp":"2026-07-16T11:15:04.024Z","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"Checking the parser"}]}}`,
		)

		Expect(uses).To(HaveLen(1))
		Expect(uses[0].Input["text"]).To(Equal("Checking the parser"))
		Expect(uses[0].Timestamp.UTC().Format(nanoLayout)).To(Equal("2026-07-16T11:15:04.024Z"))
		Expect(uses[0].Input).ToNot(HaveKey("first_at"))
		Expect(uses[0].Input).ToNot(HaveKey("last_at"))
		Expect(uses[0].Input).ToNot(HaveKey("count"))
	})

	// Regression pin: 47% of reasoning records carry no turn_id, and old-format
	// turn_context carries none either. Keying the collapse on turn_id alone
	// would fold a whole multi-turn session into one bogus span, so the flush
	// must trigger on the turn_context event itself.
	It("segments spans on turn_context when the rollout carries no turn ids", func() {
		uses := reasoningUses(
			`{"timestamp":"2026-01-28T15:32:20.000Z","type":"session_meta","payload":{"id":"sess-old","cwd":"/repo"}}`,
			`{"timestamp":"2026-01-28T15:32:24.056Z","type":"turn_context","payload":{"cwd":"/repo","approval_policy":"on-request"}}`,
			`{"timestamp":"2026-01-28T15:32:27.895Z","type":"response_item","payload":{"type":"reasoning","summary":[],"encrypted_content":"e1"}}`,
			`{"timestamp":"2026-01-28T15:32:31.990Z","type":"turn_context","payload":{"cwd":"/repo","approval_policy":"on-request"}}`,
			`{"timestamp":"2026-01-28T15:32:38.675Z","type":"response_item","payload":{"type":"reasoning","summary":[],"encrypted_content":"e2"}}`,
		)

		Expect(uses).To(HaveLen(2))
		Expect(uses[0].Timestamp.UTC().Format(nanoLayout)).To(Equal("2026-01-28T15:32:27.895Z"))
		Expect(uses[0].Input["count"]).To(Equal(1))
		Expect(uses[0].Input["text"]).To(ContainSubstring("1 encrypted reasoning record "))
		Expect(uses[1].Timestamp.UTC().Format(nanoLayout)).To(Equal("2026-01-28T15:32:38.675Z"))
		Expect(uses[1].Input["count"]).To(Equal(1))
	})

	// Plaintext and contentless reasoning genuinely mix inside one file, so the
	// collapse is per-turn rather than per-run: a plaintext row must not flush.
	It("keeps plaintext rows and adds one collapsed row for a mixed turn", func() {
		uses := reasoningUses(
			reasoningSessionMeta,
			reasoningTurnContext,
			`{"timestamp":"2026-07-16T11:15:01.000Z","type":"response_item","payload":{"type":"reasoning","summary":[],"encrypted_content":"e1"}}`,
			`{"timestamp":"2026-07-16T11:15:02.000Z","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"first plain"}]}}`,
			`{"timestamp":"2026-07-16T11:15:03.000Z","type":"response_item","payload":{"type":"reasoning","summary":[],"encrypted_content":"e2"}}`,
			`{"timestamp":"2026-07-16T11:15:04.000Z","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"second plain"}]}}`,
			`{"timestamp":"2026-07-16T11:15:05.000Z","type":"response_item","payload":{"type":"reasoning","summary":[],"encrypted_content":"e3"}}`,
		)

		Expect(uses).To(HaveLen(3))
		Expect(uses[0].Input["text"]).To(Equal("first plain"))
		Expect(uses[1].Input["text"]).To(Equal("second plain"))
		Expect(uses[2].Input["count"]).To(Equal(3))
		Expect(uses[2].Input["first_at"]).To(Equal("2026-07-16T11:15:01Z"))
		Expect(uses[2].Input["last_at"]).To(Equal("2026-07-16T11:15:05Z"))
	})

	It("flushes a pending span at EOF", func() {
		uses := reasoningUses(
			reasoningSessionMeta,
			reasoningTurnContext,
			`{"timestamp":"2026-07-16T11:15:04.024Z","type":"response_item","payload":{"type":"reasoning","summary":[],"encrypted_content":"e1"}}`,
		)

		Expect(uses).To(HaveLen(1))
		Expect(uses[0].Input["count"]).To(Equal(1))
		Expect(uses[0].Timestamp.UTC().Format(nanoLayout)).To(Equal("2026-07-16T11:15:04.024Z"))
	})

	It("drops reasoning records with no parseable timestamp", func() {
		Expect(reasoningUses(
			reasoningSessionMeta,
			reasoningTurnContext,
			`{"type":"response_item","payload":{"type":"reasoning","summary":[],"encrypted_content":"e1"}}`,
		)).To(BeEmpty())
	})
})
