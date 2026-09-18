package api_test

import (
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Codex sandbox translation", func() {
	It("preserves approval posture when no sandbox is requested", func() {
		for _, test := range []struct {
			mode     api.PermissionMode
			approval api.CodexApprovalPolicy
		}{
			{mode: api.PermissionDefault, approval: api.CodexApprovalOnRequest},
			{mode: api.PermissionBypass, approval: api.CodexApprovalNever},
		} {
			translation, err := api.TranslateCodexSandbox(
				api.RuntimeOf(api.OpenAI, api.ModeAgent), nil, test.mode,
			)

			Expect(err).NotTo(HaveOccurred())
			Expect(translation.Sandbox).To(BeEmpty())
			Expect(translation.Approval).To(Equal(test.approval))
		}
	})

	It("pins an unstated plan sandbox to read-only", func() {
		translation, err := api.TranslateCodexSandbox(api.RuntimeOf(api.OpenAI, api.ModeAgent), nil, api.PermissionPlan)
		Expect(err).NotTo(HaveOccurred())
		Expect(translation.Sandbox).To(Equal(api.CodexSandboxReadOnly))
		Expect(translation.Approval).To(Equal(api.CodexApprovalOnRequest))
	})

	It("rejects plan mode with an explicit disabled sandbox", func() {
		_, err := api.TranslateCodexSandbox(api.RuntimeOf(api.OpenAI, api.ModeAgent), &api.SandboxRef{Mode: api.SandboxOff}, api.PermissionPlan)
		Expect(err).To(MatchError(ContainSubstring("plan")))
	})
})
