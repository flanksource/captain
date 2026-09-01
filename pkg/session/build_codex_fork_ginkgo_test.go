package session

import (
	"os"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"

	"github.com/flanksource/captain/pkg/api"
)

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

func writeCodexRollout(name, content string) string {
	path := filepath.Join(ginkgo.GinkgoT().TempDir(), name)
	Expect(os.WriteFile(path, []byte(content), 0o644)).To(Succeed())
	return path
}

var _ = ginkgo.Describe("BuildCodexFile", func() {
	ginkgo.It("builds a subagent rollout as its own session linked to the parent thread", func() {
		s, err := BuildCodexFile(writeCodexRollout("rollout-child.jsonl", codexForkRollout))
		Expect(err).NotTo(HaveOccurred())
		Expect(s.ID).To(Equal("child-thread"))
		Expect(s.ForkedFrom).To(Equal("parent-thread"))
		Expect(s.CWD).To(Equal("/repo"))
		Expect(s.Root).To(PointTo(MatchFields(IgnoreExtras, Fields{
			"Type": Equal("subagent"),
			"Desc": Equal("Descartes /root/contract"),
		})))

		turnIDs := make([]string, 0, len(s.Turns))
		for _, turn := range s.Turns {
			turnIDs = append(turnIDs, turn.ID)
		}
		Expect(turnIDs).To(Equal([]string{"child-turn"}), "the parent's replayed turn must not become the child's")
	})

	ginkgo.It("names the reason a rollout yields no session", func() {
		_, err := BuildCodexFile(writeCodexRollout("empty.jsonl", ""))
		Expect(err).To(MatchError(ErrCodexEmpty))

		review := `{"type":"session_meta","payload":{"id":"review-1","cwd":"/repo"}}` + "\n" +
			`{"type":"turn_context","payload":{"model":"` + api.CodexAutoReviewModel + `","effort":"low"}}` + "\n"
		_, err = BuildCodexFile(writeCodexRollout("review.jsonl", review))
		Expect(err).To(MatchError(ErrCodexAutoReview))

		_, err = BuildCodexFile(filepath.Join(ginkgo.GinkgoT().TempDir(), "missing.jsonl"))
		Expect(err).To(MatchError(os.ErrNotExist))
	})
})
