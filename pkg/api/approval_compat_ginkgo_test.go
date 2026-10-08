package api_test

import (
	"context"
	"errors"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

type compatLog struct{ warn, debug []string }

func captureCompatLog() *compatLog {
	log := &compatLog{}
	DeferCleanup(api.SetCompatLogForTest(
		func(format string, args ...any) { log.warn = append(log.warn, fmt.Sprintf(format, args...)) },
		func(format string, args ...any) { log.debug = append(log.debug, fmt.Sprintf(format, args...)) },
	))
	return log
}

var _ = Describe("deprecated approval entry points", func() {
	approveAll := func(calls *[]api.ApprovalRequest) api.PermissionFunc {
		return func(_ context.Context, request api.PermissionRequest) (api.PermissionDecision, error) {
			*calls = append(*calls, request)
			return api.PermissionDecision{Allow: true}, nil
		}
	}
	legacyTool := api.ApprovalRequest{Tool: "Edit", Kind: api.ApprovalKindTool, ToolUseID: "toolu_1", LegacyContract: true}
	newCommand := api.ApprovalRequest{Tool: "exec_command", Kind: api.ApprovalKindCommand, ToolUseID: "appr_1",
		Command: &api.CommandApproval{Command: "git push"}}

	It("keeps the old type names assignable to the new ones", func() {
		var calls []api.ApprovalRequest
		var old api.PermissionFunc = approveAll(&calls)
		config := api.Config{OnApproval: old}
		binding, err := config.Approvals()
		Expect(err).NotTo(HaveOccurred())
		Expect(binding.Legacy).To(BeFalse())
	})

	Describe("Config.Approvals", func() {
		It("returns OnApproval unwrapped and logs nothing", func() {
			log := captureCompatLog()
			var calls []api.ApprovalRequest
			binding, err := api.Config{OnApproval: approveAll(&calls)}.Approvals()
			Expect(err).NotTo(HaveOccurred())
			Expect(binding.Legacy).To(BeFalse())
			_, err = binding.Func(context.Background(), newCommand)
			Expect(err).NotTo(HaveOccurred())
			Expect(calls).To(HaveLen(1))
			Expect(log.warn).To(BeEmpty())
		})

		It("returns an empty binding when neither field is set", func() {
			binding, err := api.Config{}.Approvals()
			Expect(err).NotTo(HaveOccurred())
			Expect(binding.Func).To(BeNil())
			Expect(binding.Legacy).To(BeFalse())
		})

		It("refuses a config that sets both fields", func() {
			var calls []api.ApprovalRequest
			_, err := api.Config{OnApproval: approveAll(&calls), CanUseTool: approveAll(&calls)}.Approvals()
			Expect(err).To(MatchError(ContainSubstring("both OnApproval and the deprecated CanUseTool")))
		})

		It("runs a CanUseTool callback in legacy mode and warns once per process", func() {
			log := captureCompatLog()
			var calls []api.ApprovalRequest
			config := api.Config{CanUseTool: approveAll(&calls)}
			binding, err := config.Approvals()
			Expect(err).NotTo(HaveOccurred())
			Expect(binding.Legacy).To(BeTrue())
			_, err = config.Approvals()
			Expect(err).NotTo(HaveOccurred())

			decision, err := binding.Func(context.Background(), legacyTool)
			Expect(err).NotTo(HaveOccurred())
			Expect(decision.Allow).To(BeTrue())

			_, err = binding.Func(context.Background(), newCommand)
			Expect(errors.Is(err, api.ErrLegacyApprovalSkipped)).To(BeTrue())
			Expect(err).To(MatchError(ContainSubstring("command request (exec_command, appr_1)")))

			Expect(calls).To(Equal([]api.ApprovalRequest{legacyTool}))
			Expect(log.warn).To(HaveLen(1))
			Expect(log.warn[0]).To(ContainSubstring("Config.CanUseTool is deprecated; use Config.OnApproval"))
			Expect(log.warn[0]).To(ContainSubstring("Legacy mode"))
		})
	})

	Describe("provider construction", func() {
		It("folds CanUseTool into a legacy-mode OnApproval", func() {
			captureCompatLog()
			var calls []api.ApprovalRequest
			config := api.Config{CanUseTool: approveAll(&calls)}
			Expect(config.ResolveApprovalsForTest()).To(Succeed())
			Expect(config.CanUseTool).To(BeNil())
			Expect(config.LegacyApprovals()).To(BeTrue())
			_, err := config.OnApproval(context.Background(), newCommand)
			Expect(errors.Is(err, api.ErrLegacyApprovalSkipped)).To(BeTrue())
			Expect(calls).To(BeEmpty())
		})

		It("keeps OnApproval as-is", func() {
			var calls []api.ApprovalRequest
			config := api.Config{OnApproval: approveAll(&calls)}
			Expect(config.ResolveApprovalsForTest()).To(Succeed())
			Expect(config.LegacyApprovals()).To(BeFalse())
			_, err := config.OnApproval(context.Background(), newCommand)
			Expect(err).NotTo(HaveOccurred())
			Expect(calls).To(HaveLen(1))
		})
	})

	Describe("WarnDeprecatedEntryPoint", func() {
		It("omits the legacy-mode clause for the broker, which is the callee", func() {
			log := captureCompatLog()
			api.WarnDeprecatedEntryPoint("Broker.CanUseTool", "gavel/pr/ui/todo.go:12")
			Expect(log.warn).To(ConsistOf(
				"captain: Broker.CanUseTool is deprecated; use Broker.OnApproval (removed in unified-approval Phase 6) (called from gavel/pr/ui/todo.go:12)"))
		})
	})

	Describe("LogLegacyApprovalSkip", func() {
		It("warns once per run and kind, then logs at debug", func() {
			log := captureCompatLog()
			skip := fmt.Errorf("command request (exec_command, appr_1) %w", api.ErrLegacyApprovalSkipped)
			api.LogLegacyApprovalSkip("run-a", api.ApprovalKindCommand, skip, "decline")
			api.LogLegacyApprovalSkip("run-a", api.ApprovalKindCommand, skip, "decline")
			api.LogLegacyApprovalSkip("run-a", api.ApprovalKindPermissions, skip, "no extra grants")
			api.LogLegacyApprovalSkip("run-b", api.ApprovalKindCommand, skip, "decline")

			Expect(log.warn).To(HaveLen(3))
			Expect(log.warn[0]).To(ContainSubstring("answered natively with decline"))
			Expect(log.warn[0]).To(ContainSubstring("Switch to Config.OnApproval"))
			Expect(log.debug).To(HaveLen(1))
		})
	})
})
