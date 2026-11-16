package approval_test

import (
	"context"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	"gorm.io/gorm"
)

var _ = Describe("Approval broker cancellation", func() {
	DescribeTable("cancels the durable row and releases the run",
		func(ctx SpecContext, duringRead bool) {
			db := openBrokerDB(ctx)
			run := newProviderRun(ctx, db)
			callCtx, cancel := context.WithCancel(ctx)
			DeferCleanup(cancel)
			broker := run.broker(time.Minute)
			surfaced := false
			queries := db.Gorm().Callback().Query()
			const callback = "test:cancel_approval_poll"
			Expect(queries.Before("gorm:query").Register(callback, func(_ *gorm.DB) {
				if duringRead && surfaced {
					cancel()
				}
			})).To(Succeed())
			DeferCleanup(func() { Expect(queries.Remove(callback)).To(Succeed()) })
			broker.Notify = func(ctx context.Context, event api.Event) error {
				if err := run.notify(ctx, event); err != nil {
					return err
				}
				surfaced = true
				if !duringRead {
					cancel()
				}
				return nil
			}

			decision, err := broker.OnApproval(callCtx, api.ApprovalRequest{
				Kind: api.ApprovalKindTool, Tool: "Bash",
				Input: map[string]any{"command": "git push"}, ToolUseID: "toolu_cancel_poll",
			})
			Expect(err).To(MatchError(context.Canceled))
			Expect(decision).To(BeZero())
			event := run.awaitPermission()
			cancelled, err := db.GetTurnRequest(ctx, uuid.MustParse(event.ApprovalID))
			Expect(err).NotTo(HaveOccurred())
			Expect(*cancelled).To(MatchFields(IgnoreExtras, Fields{
				"State":      Equal(database.TurnRequestStateCancelled),
				"Reason":     ContainSubstring(context.Canceled.Error()),
				"ResolvedAt": Not(BeNil()),
			}))
			Expect(run.state(ctx)).To(Equal(database.PromptRunStateRunning))
		},
		Entry("when cancellation precedes the first poll", false),
		Entry("when cancellation interrupts a poll's database read", true),
	)

	DescribeTable("preserves a durable decision when cancellation precedes the poll",
		func(ctx SpecContext, approved bool, state database.TurnRequestState) {
			db := openBrokerDB(ctx)
			run := newProviderRun(ctx, db)
			callCtx, cancel := context.WithCancel(ctx)
			DeferCleanup(cancel)
			broker := run.broker(time.Minute)
			var resolved *database.TurnRequest
			broker.Notify = func(ctx context.Context, event api.Event) error {
				var err error
				resolved, err = db.ResolveToolApprovalRequest(ctx, database.ResolveToolApprovalRequestInput{
					SessionID: run.session, RequestID: uuid.MustParse(event.ApprovalID),
					Approved: approved, ResolvedBy: "spec", Reason: "reviewed",
				})
				cancel()
				return err
			}

			decision, err := broker.OnApproval(callCtx, api.ApprovalRequest{
				Kind: api.ApprovalKindTool, Tool: "Bash",
				Input: map[string]any{"command": "git push"}, ToolUseID: "toolu_cancel_resolved",
			})
			Expect(err).To(MatchError(context.Canceled))
			Expect(decision).To(BeZero())
			Expect(resolved).NotTo(BeNil())
			Expect(resolved.State).To(Equal(state))
			stored, err := db.GetTurnRequest(ctx, resolved.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(stored).To(Equal(resolved))
			Expect(run.state(ctx)).To(Equal(database.PromptRunStateRunning))
		},
		Entry("for an approved request", true, database.TurnRequestStateApproved),
		Entry("for a denied request", false, database.TurnRequestStateDenied),
	)
})
