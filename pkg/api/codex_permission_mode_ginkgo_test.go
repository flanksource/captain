package api_test

import (
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Codex permission mode", func() {
	codexAgent := api.RuntimeOf(api.OpenAI, api.ModeAgent)

	DescribeTable("routes approval requests to the reviewer the posture names",
		func(mode api.PermissionMode, reviewer api.CodexApprovalsReviewer) {
			translation, err := api.TranslateCodexSandbox(codexAgent, nil, mode)

			Expect(err).NotTo(HaveOccurred())
			Expect(translation.ApprovalsReviewer).To(Equal(reviewer))
		},
		Entry("an unstated posture inherits the user's Codex configuration", api.PermissionMode(""), api.CodexApprovalsReviewer("")),
		Entry("default asks the user", api.PermissionDefault, api.CodexReviewerUser),
		Entry("acceptEdits asks the user", api.PermissionAcceptEdits, api.CodexReviewerUser),
		Entry("plan asks the user", api.PermissionPlan, api.CodexReviewerUser),
		Entry("auto hands approvals to Codex's auto-review subagent", api.PermissionAuto, api.CodexReviewerAutoReview),
		Entry("bypass asks nobody and names the user explicitly", api.PermissionBypass, api.CodexReviewerUser),
	)

	It("declares auto as a native Codex posture", func() {
		support := api.PermissionCapabilitiesFor(codexAgent).ModeSupport(api.PermissionAuto)

		Expect(support.Kind).To(Equal(api.SupportNative))
		Expect(support.Effects.Approval).To(Equal(string(api.CodexApprovalOnRequest)))
		Expect(support.Effects.Reviewer).To(Equal(string(api.CodexReviewerAutoReview)))
	})

	DescribeTable("recovers the posture a recorded Codex turn ran under",
		func(approval string, reviewer api.CodexApprovalsReviewer, collaboration string, want api.PermissionMode) {
			Expect(api.CodexPermissionMode(approval, reviewer, collaboration)).To(Equal(want))
		},
		Entry("plan collaboration mode wins over the approval policy", "on-request", api.CodexReviewerAutoReview, "plan", api.PermissionPlan),
		Entry("never-ask is bypass", "never", api.CodexReviewerUser, "default", api.PermissionBypass),
		Entry("the auto-review subagent is auto", "on-request", api.CodexReviewerAutoReview, "default", api.PermissionAuto),
		Entry("the guardian subagent is auto", "on-request", api.CodexReviewerGuardianSubagent, "", api.PermissionAuto),
		Entry("asking the user is default", "on-request", api.CodexReviewerUser, "default", api.PermissionDefault),
		Entry("an untrusted policy still asks the user", "untrusted", api.CodexApprovalsReviewer(""), "", api.PermissionDefault),
		Entry("a granular policy still asks the user", "granular", api.CodexApprovalsReviewer(""), "", api.PermissionDefault),
		Entry("a turn that recorded no policy has no posture", "", api.CodexApprovalsReviewer(""), "", api.PermissionMode("")),
	)
})
