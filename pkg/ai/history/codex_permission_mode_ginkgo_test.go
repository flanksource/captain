package history

import (
	"strings"

	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func codexPostureRollout(turnContexts ...string) string {
	lines := []string{`{"timestamp":"2026-09-14T09:00:00Z","type":"session_meta","payload":{"id":"sess-posture","cwd":"/repo"}}`}
	for _, turnContext := range turnContexts {
		lines = append(lines,
			`{"timestamp":"2026-09-14T09:00:01Z","type":"turn_context","payload":`+turnContext+`}`,
			`{"timestamp":"2026-09-14T09:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}}`,
		)
	}
	return strings.Join(lines, "\n")
}

func codexUsePermissionModes(uses []ToolUse) []api.PermissionMode {
	modes := make([]api.PermissionMode, 0, len(uses))
	for _, use := range uses {
		modes = append(modes, use.PermissionMode)
	}
	return modes
}

var _ = Describe("Codex rollout permission posture", func() {
	DescribeTable("stamps the posture a turn_context declares onto the turn's rows",
		func(turnContext string, want api.PermissionMode) {
			uses, err := ExtractCodexToolUsesFromReader(strings.NewReader(codexPostureRollout(turnContext)))

			Expect(err).NotTo(HaveOccurred())
			Expect(codexUsePermissionModes(uses)).To(Equal([]api.PermissionMode{want}))
		},
		Entry("auto review answers approvals",
			`{"turn_id":"t1","approval_policy":"on-request","approvals_reviewer":"auto_review","collaboration_mode":{"mode":"default"}}`,
			api.PermissionAuto),
		Entry("the user answers approvals",
			`{"turn_id":"t1","approval_policy":"on-request","approvals_reviewer":"user","collaboration_mode":{"mode":"default"}}`,
			api.PermissionDefault),
		Entry("approvals are never requested, whoever would review them",
			`{"turn_id":"t1","approval_policy":"never","approvals_reviewer":"auto_review","collaboration_mode":{"mode":"default"}}`,
			api.PermissionBypass),
		Entry("plan collaboration mode",
			`{"turn_id":"t1","approval_policy":"on-request","approvals_reviewer":"auto_review","collaboration_mode":{"mode":"plan"}}`,
			api.PermissionPlan),
		Entry("a granular approval policy reviewed by the user",
			`{"turn_id":"t1","approval_policy":{"granular":{"sandbox_approval":true}},"approvals_reviewer":"user"}`,
			api.PermissionDefault),
		Entry("a rollout that predates posture fields", `{"turn_id":"t1","model":"gpt-5.6-sol"}`, api.PermissionMode("")),
	)

	It("keeps each turn's own posture as the posture changes mid-session", func() {
		rollout := codexPostureRollout(
			`{"turn_id":"t1","approval_policy":"on-request","approvals_reviewer":"user","collaboration_mode":{"mode":"default"}}`,
			`{"turn_id":"t2","approval_policy":"on-request","approvals_reviewer":"auto_review","collaboration_mode":{"mode":"default"}}`,
			`{"turn_id":"t3","model":"gpt-5.6-sol"}`,
		)

		uses, err := ExtractCodexToolUsesFromReader(strings.NewReader(rollout))

		Expect(err).NotTo(HaveOccurred())
		Expect(codexUsePermissionModes(uses)).To(Equal([]api.PermissionMode{
			api.PermissionDefault, api.PermissionAuto, api.PermissionAuto,
		}))
	})
})
