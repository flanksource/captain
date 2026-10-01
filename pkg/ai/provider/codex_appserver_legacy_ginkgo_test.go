package provider

import (
	"encoding/json"

	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// COMPAT(unified-approval): the Phase 4 behaviour fixture for a deprecated
// approve-all CanUseTool; deleted in Phase 6 with the shim.
var _ = Describe("Codex app-server with a deprecated approve-all CanUseTool", func() {
	command := `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","approvalId":"appr-1","kind":"command","command":"git push","cwd":"/repo/gavel","startedAtMs":1}`
	fileChange := `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-5","reason":null,"grantRoot":null,"startedAtMs":2}`
	permissions := `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-7","cwd":"/repo/gavel","startedAtMs":1,
		"permissions":{"network":{"enabled":true}}}`
	question := `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-9","isBlocking":true,"questions":[{"id":"location","header":"Plan","question":"Where should the plan go?","isOther":true}]}`
	fullAccess := workspaceRun
	fullAccess.Sandbox = &api.SandboxRef{Mode: api.SandboxOff}
	fullAccess.Permissions = api.Permissions{Mode: api.PermissionBypass}

	DescribeTable("answers command, file-change and permission requests from the posture, as before the rename",
		func(fullAccessRun bool, commandAnswer, fileAnswer string, grant map[string]any) {
			run := workspaceRun
			if fullAccessRun {
				run = fullAccess
			}
			legacy := &approvalRecorder{decision: api.ApprovalDecision{Allow: true}}
			c, _ := codexApprovalHarness(api.LegacyApprovalFunc(legacy.approve, "Config.CanUseTool", ""), run)
			c.handleNotification("item/started", json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","startedAtMs":1,
				"item":{"type":"fileChange","id":"item-5","status":"inProgress","changes":[{"path":"/repo/gavel/a.go","kind":{"type":"add"},"diff":"+a"}]}}`))

			answer, rpcErr := c.handleApproval(codexServerRequest(`1`, "item/commandExecution/requestApproval", command))
			Expect(rpcErr).To(BeNil())
			Expect(answer).To(Equal(map[string]string{"decision": commandAnswer}))

			answer, rpcErr = c.handleApproval(codexServerRequest(`2`, "item/fileChange/requestApproval", fileChange))
			Expect(rpcErr).To(BeNil())
			Expect(answer).To(Equal(map[string]string{"decision": fileAnswer}))

			answer, rpcErr = c.handleApproval(codexServerRequest(`3`, "item/permissions/requestApproval", permissions))
			Expect(rpcErr).To(BeNil())
			Expect(answer).To(Equal(grant))

			Expect(legacy.received()).To(BeEmpty(), "the pre-rename contract never sent these to CanUseTool")
		},
		Entry("a workspace run declines", false, "decline", "decline", map[string]any{"permissions": map[string]any{}, "scope": "turn"}),
		Entry("a full-access run accepts", true, "accept", "accept", map[string]any{"permissions": map[string]any{}, "scope": "turn"}),
	)

	It("still sends a question to the callback and returns its answers", func() {
		legacy := &approvalRecorder{decision: api.ApprovalDecision{Allow: true, UpdatedInput: map[string]any{"answers": map[string]any{"location": "Inline"}}}}
		c, _ := codexApprovalHarness(api.LegacyApprovalFunc(legacy.approve, "Config.CanUseTool", ""), workspaceRun)

		answer, rpcErr := c.handleApproval(codexServerRequest(`4`, "item/tool/requestUserInput", question))

		Expect(rpcErr).To(BeNil())
		Expect(answer).To(Equal(map[string]any{"answers": map[string]any{"location": map[string]any{"answers": []string{"Inline"}}}}))
		Expect(legacy.received()).To(HaveLen(1))
		Expect(legacy.received()[0].Tool).To(Equal("AskUserQuestion"))
	})
})
