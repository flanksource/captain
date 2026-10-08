package approval_test

import (
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
)

var _ = Describe("Deprecated Broker.CanUseTool", Ordered, func() {
	var db *database.DB

	BeforeAll(func(ctx SpecContext) {
		db = openBrokerDB(ctx)
	})

	It("brokers a request built before Kind existed as a tool approval", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		broker := run.broker(time.Minute)
		outcomes := make(chan outcome, 1)
		go func() {
			defer GinkgoRecover()
			decision, err := broker.CanUseTool(ctx, api.PermissionRequest{
				Tool: "Read", Input: map[string]any{"file_path": "a.go"}, ToolUseID: "toolu_legacy",
			})
			outcomes <- outcome{decision: decision, err: err}
		}()

		event := run.awaitPermission()
		_, err := db.ResolveToolApprovalRequest(ctx, database.ResolveToolApprovalRequestInput{
			SessionID: run.session, RequestID: uuid.MustParse(event.ApprovalID), Approved: true, ResolvedBy: "dashboard",
		})
		Expect(err).NotTo(HaveOccurred())
		Eventually(outcomes).Should(Receive(Equal(outcome{decision: api.ApprovalDecision{Allow: true}})))
	})
})
