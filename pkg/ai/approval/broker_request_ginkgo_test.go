package approval_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
)

var _ = Describe("Broker request checks", Ordered, func() {
	var db *database.DB

	BeforeAll(func(ctx SpecContext) {
		db = openBrokerDB(ctx)
	})

	It("refuses a secret question before recording anything", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		_, err := run.broker(time.Minute).OnApproval(ctx, api.ApprovalRequest{
			Tool: "AskUserQuestion", Kind: api.ApprovalKindQuestion, ToolUseID: "item_secret",
			Questions: []api.TerminalQuestion{{ID: "token", Text: "API token?", Secret: true}},
		})
		Expect(err).To(MatchError(ContainSubstring(`question "token" is secret`)))
		Consistently(run.events).ShouldNot(Receive())
	})

	It("rejects a request without a kind", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		_, err := run.broker(time.Minute).OnApproval(ctx, api.ApprovalRequest{Tool: "Read", ToolUseID: "toolu_kindless"})
		Expect(err).To(MatchError(ContainSubstring("invalid kind")))
	})
})
