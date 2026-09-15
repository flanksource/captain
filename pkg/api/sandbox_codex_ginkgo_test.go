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
})
