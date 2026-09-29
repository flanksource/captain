package promptrun_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/promptrun"
	"github.com/google/uuid"
)

var _ = Describe("settling a prompt run outside Run", func() {
	var (
		db  *database.DB
		run *database.PromptRun
	)

	BeforeEach(func(ctx SpecContext) {
		db = openRecordDB()
		session, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ID: uuid.New(), Source: "gavel", Provider: "cmux-claude", HostID: "record-control-test",
		})
		Expect(err).NotTo(HaveOccurred())
		run, err = db.CreatePromptRun(ctx, database.CreatePromptRunInput{SessionID: session.ID})
		Expect(err).NotTo(HaveOccurred())
	})

	It("fails a run that never started, promoting a queued phase to generate", func(ctx SpecContext) {
		failed, err := promptrun.Fail(ctx, db, run.ID, "dispatcher exited before the agent started")
		Expect(err).NotTo(HaveOccurred())
		Expect(failed.State).To(Equal(database.PromptRunStateFailed))
		Expect(failed.Phase).To(Equal(database.PromptRunPhaseGenerate))
		Expect(failed.Error).To(Equal("dispatcher exited before the agent started"))
		Expect(failed.FinishedAt).NotTo(BeNil())
	})

	It("refuses to settle a run that already finished", func(ctx SpecContext) {
		_, err := promptrun.Fail(ctx, db, run.ID, "first")
		Expect(err).NotTo(HaveOccurred())
		_, err = promptrun.Cancel(ctx, db, run.ID, "second")
		Expect(err).To(MatchError(promptrun.ErrRunFinished))
	})

	It("refuses a blank reason", func(ctx SpecContext) {
		_, err := promptrun.Fail(ctx, db, run.ID, "  ")
		Expect(err).To(MatchError(ContainSubstring("reason")))
	})

	It("cancels the run together with its pending tool approvals", func(ctx SpecContext) {
		request, err := db.CreateToolApprovalRequest(ctx, database.CreateToolApprovalRequestInput{
			SessionID: run.SessionID, PromptRunID: run.ID, ToolCallID: "call-1", Tool: "Bash",
			Input: map[string]any{"command": "rm -rf build"}, ExpiresAt: time.Now().Add(time.Hour),
		})
		Expect(err).NotTo(HaveOccurred())

		cancelled, err := promptrun.Cancel(ctx, db, run.ID, "run stopped")
		Expect(err).NotTo(HaveOccurred())
		Expect(cancelled.State).To(Equal(database.PromptRunStateCancelled))
		Expect(cancelled.Error).To(Equal("run stopped"))

		requests, err := db.ListTurnRequests(ctx, database.TurnRequestFilter{SessionID: run.SessionID, PromptRunID: &run.ID})
		Expect(err).NotTo(HaveOccurred())
		Expect(requests).To(HaveLen(1))
		Expect(requests[0].ID).To(Equal(request.ID))
		Expect(requests[0].State).To(Equal(database.TurnRequestStateCancelled))
	})

	Describe("Settle, for a parked run whose session was continued elsewhere", func() {
		const (
			parkedQuestion = "Which database should the migration target?"
			followupReply  = "Migrated the staging database."
		)

		BeforeEach(func(ctx SpecContext) {
			waiting, phase := database.PromptRunStateWaiting, database.PromptRunPhaseGenerate
			envelope := map[string]any{"endStatus": "ask", "questions": []any{map[string]any{"text": parkedQuestion}}}
			var err error
			run, err = db.UpdatePromptRun(ctx, database.UpdatePromptRunInput{
				ID: run.ID, ExpectedVersion: run.Version, State: &waiting, Phase: &phase, ResultJSON: &envelope,
			})
			Expect(err).NotTo(HaveOccurred())
		})

		It("files a succeeded turn with its reply, replacing the envelope that parked it", func(ctx SpecContext) {
			settled, err := promptrun.Settle(ctx, db, run.ID, promptrun.Outcome{
				State: database.PromptRunStateSucceeded, Phase: database.PromptRunPhaseFinished, Text: followupReply,
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(settled.State).To(Equal(database.PromptRunStateSucceeded))
			Expect(settled.Phase).To(Equal(database.PromptRunPhaseFinished))
			Expect(settled.ResultText).To(Equal(followupReply))
			Expect(settled.ResultJSON).To(BeNil(), "the parked ask envelope is not the turn's result")
			Expect(settled.Error).To(BeEmpty())
			Expect(settled.FinishedAt).NotTo(BeNil())
		})

		It("parks the run again on the turn's new envelope, keeping its phase", func(ctx SpecContext) {
			followup := map[string]any{"endStatus": "ask", "questions": []any{map[string]any{"text": "Which schema?"}}}

			settled, err := promptrun.Settle(ctx, db, run.ID, promptrun.Outcome{
				State: database.PromptRunStateWaiting, JSON: followup,
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(settled.State).To(Equal(database.PromptRunStateWaiting))
			Expect(settled.Phase).To(Equal(database.PromptRunPhaseGenerate))
			Expect(settled.ResultJSON).To(Equal(followup))
			Expect(settled.FinishedAt).To(BeNil())
		})

		It("files a failed turn with its error", func(ctx SpecContext) {
			settled, err := promptrun.Settle(ctx, db, run.ID, promptrun.Outcome{
				State: database.PromptRunStateFailed, Error: "the continued turn crashed",
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(settled.State).To(Equal(database.PromptRunStateFailed))
			Expect(settled.Error).To(Equal("the continued turn crashed"))
			Expect(settled.FinishedAt).NotTo(BeNil())
		})

		It("files a cancelled turn and cancels the run's pending tool approvals", func(ctx SpecContext) {
			_, err := db.CreateToolApprovalRequest(ctx, database.CreateToolApprovalRequestInput{
				SessionID: run.SessionID, PromptRunID: run.ID, ToolCallID: "call-parked", Tool: "Bash",
				Input: map[string]any{"command": "make migrate"}, ExpiresAt: time.Now().Add(time.Hour),
			})
			Expect(err).NotTo(HaveOccurred())

			settled, err := promptrun.Settle(ctx, db, run.ID, promptrun.Outcome{
				State: database.PromptRunStateCancelled, Error: "the continued turn was stopped",
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(settled.State).To(Equal(database.PromptRunStateCancelled))
			requests, err := db.ListTurnRequests(ctx, database.TurnRequestFilter{SessionID: run.SessionID, PromptRunID: &run.ID})
			Expect(err).NotTo(HaveOccurred())
			Expect(requests).To(HaveLen(1))
			Expect(requests[0].State).To(Equal(database.TurnRequestStateCancelled))
		})

		It("refuses a run that already finished", func(ctx SpecContext) {
			_, err := promptrun.Settle(ctx, db, run.ID, promptrun.Outcome{State: database.PromptRunStateSucceeded, Text: followupReply})
			Expect(err).NotTo(HaveOccurred())

			_, err = promptrun.Settle(ctx, db, run.ID, promptrun.Outcome{State: database.PromptRunStateFailed, Error: "late"})

			Expect(err).To(MatchError(promptrun.ErrRunFinished))
		})

		DescribeTable("refuses an outcome it cannot file",
			func(ctx SpecContext, outcome promptrun.Outcome, reason string) {
				_, err := promptrun.Settle(ctx, db, run.ID, outcome)
				Expect(err).To(MatchError(ContainSubstring(reason)))

				unchanged, err := db.GetPromptRun(ctx, run.ID)
				Expect(err).NotTo(HaveOccurred())
				Expect(unchanged.State).To(Equal(database.PromptRunStateWaiting))
			},
			Entry("a state that is not an outcome", promptrun.Outcome{State: database.PromptRunStateRunning}, "cannot settle"),
			Entry("no state", promptrun.Outcome{}, "cannot settle"),
			Entry("a failure with no error", promptrun.Outcome{State: database.PromptRunStateFailed, Error: " "}, "reason"),
			Entry("a cancellation with no error", promptrun.Outcome{State: database.PromptRunStateCancelled}, "reason"),
		)
	})
})
