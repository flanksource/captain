package session

import (
	"strings"

	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/api"
)

// codexPostureSwitchRollout starts under user review and switches to auto
// review on its second turn, the way a live permission switch lands.
const codexPostureSwitchRollout = `{"timestamp":"2026-09-14T09:00:00Z","type":"session_meta","payload":{"id":"posture-thread","cwd":"/repo"}}
{"timestamp":"2026-09-14T09:00:01Z","type":"turn_context","payload":{"turn_id":"t1","model":"gpt-5.6-sol","approval_policy":"on-request","approvals_reviewer":"user","collaboration_mode":{"mode":"default"}}}
{"timestamp":"2026-09-14T09:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"start"}]}}
{"timestamp":"2026-09-14T09:00:03Z","type":"turn_context","payload":{"turn_id":"t2","model":"gpt-5.6-sol","approval_policy":"on-request","approvals_reviewer":"auto_review","collaboration_mode":{"mode":"default"}}}
{"timestamp":"2026-09-14T09:00:04Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}}
`

var _ = ginkgo.Describe("Codex session permission mode", func() {
	ginkgo.It("reports the posture of the rollout's latest turn", func() {
		s, err := BuildCodexFile(writeCodexRollout("rollout-posture.jsonl", codexPostureSwitchRollout))

		Expect(err).NotTo(HaveOccurred())
		Expect(s.PermissionMode).To(Equal(api.PermissionAuto))
	})

	ginkgo.It("reports the latest posture from the monitor's incremental accumulator", func() {
		parser := history.NewCodexParser()
		accumulator := NewCodexAccumulator("rollout-posture.jsonl")
		for _, line := range strings.Split(strings.TrimSpace(codexPostureSwitchRollout), "\n") {
			accumulator.Add(nil, parser.ConsumeLine(line))
		}

		Expect(accumulator.Project(parser.Snapshot()).PermissionMode).To(Equal(api.PermissionAuto))
	})
})
