package promptrun

import (
	"context"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	g "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = g.Describe("Captain approval binding", func() {
	g.It("binds an admitted run and resolves a caller tool whose callback uses a fresh context", func(ctx g.SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_promptrun_approvals"})
		db, err := database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		g.DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
		events := make(chan ai.Event, 2)
		recording := &Recording{
			DB: db, Placement: Placement{Sessions: []database.CreateSessionInput{{
				ID: uuid.New(), Source: "gavel", Provider: "todos", HostID: "approval-binding", CWD: "/work/repo",
			}}},
			Origin: "gavel.todos", PromptRunID: uuid.New(),
		}
		in := Input{
			Resolved: api.ResolvedSpec{Spec: api.Spec{
				Prompt:      api.Prompt{User: "review the change"},
				Permissions: api.Permissions{ApprovalTimeout: "10s"},
			}},
			Record: recording, Approvals: &ApprovalOptions{RequestedBy: "gavel-dashboard"},
			OnEvent: func(_ int, event ai.Event) { events <- event },
		}
		run, err := Admit(ctx, in)
		Expect(err).NotTo(HaveOccurred())
		runCtx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		Expect(bindApprovals(runCtx, &in, newRecorder(ctx, recording, run))).To(Succeed())
		outcomes := make(chan struct {
			decision api.ApprovalDecision
			err      error
		}, 1)
		go func() {
			decision, err := in.Config.OnApproval(context.Background(), api.ApprovalRequest{
				Kind: api.ApprovalKindTool, Tool: "Bash", Input: map[string]any{"command": "pwd"}, ToolUseID: "toolu_review",
			})
			outcomes <- struct {
				decision api.ApprovalDecision
				err      error
			}{decision, err}
		}()
		var event ai.Event
		Eventually(events, 5*time.Second).Should(Receive(&event))
		Expect(event.ApprovalID).NotTo(BeEmpty())
		request, err := db.GetTurnRequest(ctx, uuid.MustParse(event.ApprovalID))
		Expect(err).NotTo(HaveOccurred())
		Expect(request.SessionID).To(Equal(run.SessionID))
		Expect(*request.PromptRunID).To(Equal(run.ID))
		Expect(request.RequestedBy).To(Equal("gavel-dashboard"))
		Expect(request.ExpiresAt).NotTo(BeNil())
		Expect(*request.ExpiresAt).To(BeTemporally("~", time.Now().Add(10*time.Second), time.Second))
		_, err = db.ResolveToolApprovalRequest(ctx, database.ResolveToolApprovalRequestInput{
			SessionID: run.SessionID, RequestID: request.ID, Approved: true, ResolvedBy: "dashboard",
		})
		Expect(err).NotTo(HaveOccurred())
		var got struct {
			decision api.ApprovalDecision
			err      error
		}
		Eventually(outcomes).Should(Receive(&got))
		Expect(got.err).NotTo(HaveOccurred())
		Expect(got.decision.Allow).To(BeTrue())

		go func() {
			decision, err := in.Config.OnApproval(context.Background(), api.ApprovalRequest{
				Kind: api.ApprovalKindTool, Tool: "Write", Input: map[string]any{"path": "README.md"}, ToolUseID: "toolu_cancel",
			})
			outcomes <- struct {
				decision api.ApprovalDecision
				err      error
			}{decision, err}
		}()
		Eventually(events, 5*time.Second).Should(Receive(&event))
		cancel()
		Eventually(outcomes, 3*time.Second).Should(Receive(&got))
		Expect(got.err).To(MatchError(ContainSubstring("cancel")))
	})
})
