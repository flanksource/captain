package history

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Codex rollouts are tailed: the monitor re-parses the whole file on every
// append and writes only what is past its high-water mark. That is sound only if
// re-parsing a grown transcript reproduces the earlier rows unchanged, at the
// same keys. It did not -- reasoning spans grew on every pass, which renumbered
// every later row, and the mark then discarded the renumbered ones for good:
// 26 242 messages missing across 1 036 of 1 840 sessions.
//
// This pins the property directly rather than any one of its symptoms.

// incrementalFixture is one rollout, line by line, so a prefix is just a slice.
// It carries the three shapes that used to shift under re-parse: a run of
// contentless reasoning, a tool call whose output arrives in a later append, and
// a second reasoning run after them.
var incrementalFixture = []string{
	`{"timestamp":"2026-07-16T11:14:45.000Z","type":"session_meta","payload":{"id":"sess-inc","cwd":"/repo","cli_version":"0.160.0"}}`,
	`{"timestamp":"2026-07-16T11:14:46.000Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.5","effort":"high"}}`,
	`{"timestamp":"2026-07-16T11:14:47.000Z","type":"response_item","payload":{"type":"reasoning","summary":[],"encrypted_content":"e1","internal_chat_message_metadata_passthrough":{"turn_id":"turn-1"}}}`,
	`{"timestamp":"2026-07-16T11:14:48.000Z","type":"response_item","payload":{"type":"reasoning","summary":[],"encrypted_content":"e2","internal_chat_message_metadata_passthrough":{"turn_id":"turn-1"}}}`,
	`{"timestamp":"2026-07-16T11:14:49.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Checking the tree"}]}}`,
	`{"timestamp":"2026-07-16T11:14:50.000Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"git status --short\"}","call_id":"call-1"}}`,
	`{"timestamp":"2026-07-16T11:14:51.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"M pkg/session/pretty.go"}}`,
	`{"timestamp":"2026-07-16T11:14:52.000Z","type":"response_item","payload":{"type":"reasoning","summary":[],"encrypted_content":"e3","internal_chat_message_metadata_passthrough":{"turn_id":"turn-1"}}}`,
	`{"timestamp":"2026-07-16T11:14:53.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"One file changed"}]}}`,
}

func parseIncremental(lines []string) []ToolUse {
	uses, err := ExtractCodexToolUsesFromReader(strings.NewReader(strings.Join(lines, "\n")))
	Expect(err).ToNot(HaveOccurred())
	return uses
}

// settledRows indexes the rows a pass considers final by their source line.
// Provisional rows are excluded: they are the ones a later append is allowed to
// change, and the stable key is what lets that append update them in place.
func settledRows(uses []ToolUse) map[int64]ToolUse {
	rows := map[int64]ToolUse{}
	for _, use := range uses {
		if use.Provisional {
			continue
		}
		Expect(use.SourceLine).ToNot(BeZero(), "row %q has no line key", use.Tool)
		Expect(rows).ToNot(HaveKey(use.SourceLine), "two rows share line %d", use.SourceLine)
		rows[use.SourceLine] = use
	}
	return rows
}

var _ = Describe("incremental Codex re-parse", func() {
	// The property the whole ingest path rests on. Every prefix is a state the
	// monitor really observed, so every prefix has to hold it, not just one.
	It("reproduces every settled row of a prefix parse when the transcript grows", func() {
		full := settledRows(parseIncremental(incrementalFixture))

		for length := 3; length < len(incrementalFixture); length++ {
			prefix := settledRows(parseIncremental(incrementalFixture[:length]))

			for line, earlier := range prefix {
				later, ok := full[line]
				Expect(ok).To(BeTrue(),
					"line %d produced a settled row after %d lines and none after the full file",
					line, length)
				Expect(later.Tool).To(Equal(earlier.Tool),
					"line %d changed tool between a %d-line parse and the full file", line, length)
				Expect(later.Input).To(Equal(earlier.Input),
					"line %d changed input between a %d-line parse and the full file", line, length)
			}
		}
	})

	// F3: a call parsed before its result was written is correct at that instant
	// and wrong forever after. It stays keyed to its own line so the pass that
	// sees the output upserts the completed row rather than appending beside it.
	It("completes a tool call whose output arrives in a later append", func() {
		callLine := int64(6)

		truncated := parseIncremental(incrementalFixture[:6])
		var pending ToolUse
		for _, use := range truncated {
			if use.SourceLine == callLine {
				pending = use
			}
		}
		Expect(pending.Tool).To(Equal("Bash"))
		Expect(pending.Response).To(BeEmpty())
		Expect(pending.Provisional).To(BeTrue(), "an unpaired call must not be sealed behind the mark")

		var completed ToolUse
		for _, use := range parseIncremental(incrementalFixture) {
			if use.SourceLine == callLine {
				completed = use
			}
		}
		Expect(completed.Tool).To(Equal("Bash"))
		Expect(completed.Response).To(Equal("M pkg/session/pretty.go"))
		Expect(completed.Provisional).To(BeFalse())
	})

	// A tool_search_call is the one call whose payload lives entirely on its
	// output, so the flush at EOF has nothing to describe it with. It still has to
	// produce a provisional row under the same tool name as the completed pair --
	// a row that changes identity when it completes is a row the tail parse
	// cannot upsert, and no row at all lets the mark seal past the call's line and
	// discard the completed pair for good.
	It("keeps a tool_search call's identity when its output arrives in a later append", func() {
		lines := []string{
			incrementalFixture[0],
			incrementalFixture[1],
			`{"timestamp":"2026-07-16T11:14:47.000Z","type":"response_item","payload":{"type":"tool_search_call","arguments":"{\"query\":\"notebook\"}","call_id":"call-ts"}}`,
			`{"timestamp":"2026-07-16T11:14:48.000Z","type":"response_item","payload":{"type":"tool_search_output","call_id":"call-ts","tools":[{"tools":[{"name":"NotebookEdit"}]}]}}`,
		}
		callLine := int64(3)

		rowAt := func(uses []ToolUse) ToolUse {
			var found ToolUse
			for _, use := range uses {
				if use.SourceLine == callLine {
					found = use
				}
			}
			return found
		}

		pending := rowAt(parseIncremental(lines[:3]))
		Expect(pending.Tool).To(Equal("DeferredToolsDelta"))
		Expect(pending.Provisional).To(BeTrue(), "an unpaired tool_search call must not be sealed behind the mark")
		Expect(pending.Input).ToNot(HaveKey("addedNames"), "the resolved tools are not known until the output arrives")

		completed := rowAt(parseIncremental(lines))
		Expect(completed.Tool).To(Equal(pending.Tool), "the completed row must upsert the provisional one, not appear beside it")
		Expect(completed.Provisional).To(BeFalse())
		Expect(completed.Input["addedNames"]).To(Equal([]string{"NotebookEdit"}))
	})

	// F2: the span that used to grow on every pass. Re-parsing the full file must
	// yield one settled span of two records at line 3, not a longer span with a
	// different count and therefore a different dedupe key.
	It("keeps a reasoning span final once a non-reasoning record has closed it", func() {
		for length := 5; length <= len(incrementalFixture); length++ {
			span, ok := settledRows(parseIncremental(incrementalFixture[:length]))[3]
			Expect(ok).To(BeTrue(), "the first span is unsettled after %d lines", length)
			Expect(span.Tool).To(Equal("Reasoning"))
			Expect(span.Input["count"]).To(Equal(2),
				"the first span grew to %v records after %d lines", span.Input["count"], length)
		}
	})
})
