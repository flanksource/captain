package api_test

import (
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Codex sandbox translation", func() {
	// With no sandbox requested, a posture that lets the agent edit its workspace
	// must say so: codex's own default is read-only in a clean CODEX_HOME.
	DescribeTable("states the sandbox a posture implies when no sandbox is requested",
		func(mode api.PermissionMode, sandbox api.CodexSandbox, approval api.CodexApprovalPolicy) {
			translation, err := api.TranslateCodexSandbox(api.RuntimeOf(api.OpenAI, api.ModeAgent), nil, mode)

			Expect(err).NotTo(HaveOccurred())
			Expect([]any{translation.Sandbox, translation.Approval}).To(Equal([]any{sandbox, approval}))
		},
		Entry("an unstated posture inherits the user's Codex sandbox", api.PermissionMode(""), api.CodexSandbox(""), api.CodexApprovalOnRequest),
		Entry("default inherits the user's Codex sandbox", api.PermissionDefault, api.CodexSandbox(""), api.CodexApprovalOnRequest),
		Entry("plan is read-only", api.PermissionPlan, api.CodexSandboxReadOnly, api.CodexApprovalOnRequest),
		Entry("acceptEdits writes its workspace", api.PermissionAcceptEdits, api.CodexSandboxWorkspaceWrite, api.CodexApprovalOnRequest),
		Entry("auto writes its workspace", api.PermissionAuto, api.CodexSandboxWorkspaceWrite, api.CodexApprovalOnRequest),
		Entry("bypass writes its workspace without asking, still sandboxed", api.PermissionBypass, api.CodexSandboxWorkspaceWrite, api.CodexApprovalNever),
	)

	It("rejects plan mode with an explicit disabled sandbox", func() {
		_, err := api.TranslateCodexSandbox(api.RuntimeOf(api.OpenAI, api.ModeAgent), &api.SandboxRef{Mode: api.SandboxOff}, api.PermissionPlan)
		Expect(err).To(MatchError(ContainSubstring("plan")))
	})
})
