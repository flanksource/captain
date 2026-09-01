package history

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

// codexForkRollout is the shape Codex writes for a subagent thread: its own
// session_meta (ordinal 0) with the ordinal where its history starts, then the
// parent's session_meta and history replayed for context, then its own turns.
const codexForkRollout = `{"timestamp":"2026-08-20T09:29:32.898Z","ordinal":0,"type":"session_meta","payload":{"id":"child-thread","session_id":"parent-thread","forked_from_id":"parent-thread","parent_thread_id":"parent-thread","cwd":"/repo","thread_source":"subagent","agent_nickname":"Descartes","agent_path":"/root/contract","subagent_history_start_ordinal":5}}
{"timestamp":"2026-08-20T09:29:32.898Z","ordinal":1,"type":"session_meta","payload":{"id":"parent-thread","session_id":"parent-thread","cwd":"/elsewhere","thread_source":"user"}}
{"timestamp":"2026-08-20T09:29:32.899Z","ordinal":2,"type":"turn_context","payload":{"turn_id":"parent-turn","model":"gpt-5","effort":"medium"}}
{"timestamp":"2026-08-20T09:29:32.899Z","ordinal":3,"type":"event_msg","payload":{"type":"task_started","turn_id":"parent-turn"}}
{"timestamp":"2026-08-20T09:29:32.900Z","ordinal":4,"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"parent answered"}]}}
{"timestamp":"2026-08-20T09:29:35.000Z","ordinal":5,"type":"event_msg","payload":{"type":"task_started","turn_id":"child-turn"}}
{"timestamp":"2026-08-20T09:29:35.001Z","ordinal":6,"type":"turn_context","payload":{"turn_id":"child-turn","model":"gpt-5","effort":"medium"}}
{"timestamp":"2026-08-20T09:29:36.000Z","ordinal":7,"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"child answered"}]}}
{"timestamp":"2026-08-20T09:29:37.000Z","ordinal":8,"type":"event_msg","payload":{"type":"task_complete","turn_id":"child-turn"}}
`

const codexTopLevelRollout = `{"timestamp":"2026-08-20T09:00:00.000Z","ordinal":0,"type":"session_meta","payload":{"id":"parent-thread","session_id":"parent-thread","cwd":"/elsewhere","thread_source":"user"}}
{"timestamp":"2026-08-20T09:00:00.001Z","ordinal":1,"type":"turn_context","payload":{"turn_id":"parent-turn","model":"gpt-5","effort":"medium"}}
`

func writeRollout(content string) string {
	path := filepath.Join(GinkgoT().TempDir(), "rollout.jsonl")
	Expect(os.WriteFile(path, []byte(content), 0o644)).To(Succeed())
	return path
}

var _ = Describe("codex forked and subagent rollouts", func() {
	It("keeps the thread's own identity and drops the replayed parent history", func() {
		uses, err := ExtractCodexToolUsesFromReader(strings.NewReader(codexForkRollout))
		Expect(err).NotTo(HaveOccurred())
		Expect(uses).NotTo(BeEmpty())

		var texts, turns []string
		for _, use := range uses {
			Expect(use.SessionID).To(Equal("child-thread"), "%s use filed under the wrong thread", use.Tool)
			Expect(use.CWD).To(Equal("/repo"))
			turns = append(turns, use.TurnID)
			if text, ok := use.Input["text"].(string); ok {
				texts = append(texts, text)
			}
		}
		Expect(texts).To(ConsistOf("child answered"))
		Expect(turns).NotTo(ContainElement("parent-turn"))
		Expect(turns).To(ContainElement("child-turn"))
	})

	It("reads the fork linkage from the first session_meta only", func() {
		info, err := ReadCodexSessionInfo(writeRollout(codexForkRollout))
		Expect(err).NotTo(HaveOccurred())
		Expect(*info).To(MatchFields(IgnoreExtras, Fields{
			"ID":                  Equal("child-thread"),
			"CWD":                 Equal("/repo"),
			"ParentThreadID":      Equal("parent-thread"),
			"ThreadSource":        Equal("subagent"),
			"AgentNickname":       Equal("Descartes"),
			"AgentPath":           Equal("/root/contract"),
			"HistoryStartOrdinal": Equal(5),
		}))
		Expect(info.IsFork()).To(BeTrue())

		top, err := ReadCodexSessionInfo(writeRollout(codexTopLevelRollout))
		Expect(err).NotTo(HaveOccurred())
		Expect(top.ID).To(Equal("parent-thread"))
		Expect(top.IsFork()).To(BeFalse())
	})
})

var _ = Describe("codex rollout lines beyond the old scanner cap", func() {
	It("parses a record larger than 10 MiB instead of ending the file there", func() {
		const size = 11 << 20
		huge := strings.Repeat("y", size)
		rollout := codexTopLevelRollout +
			`{"timestamp":"2026-08-20T09:00:01.000Z","ordinal":2,"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + huge + `"}]}}` + "\n" +
			`{"timestamp":"2026-08-20T09:00:02.000Z","ordinal":3,"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"after the big one"}]}}` + "\n"

		uses, err := ExtractCodexToolUsesFromReader(strings.NewReader(rollout))
		Expect(err).NotTo(HaveOccurred())

		var texts []string
		for _, use := range uses {
			if text, ok := use.Input["text"].(string); ok {
				texts = append(texts, text)
			}
		}
		Expect(texts).To(HaveLen(2))
		Expect(texts[0]).To(HaveLen(size))
		Expect(texts[1]).To(Equal("after the big one"))
	})
})
