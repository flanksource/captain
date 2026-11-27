package promptrun_test

import (
	"errors"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/promptrun"
	"github.com/flanksource/captain/pkg/runowner"
	"github.com/google/uuid"
)

// exitedDriver is a driver identity on this host whose process ran and was
// reaped: a host that died before settling the runs it admitted.
func exitedDriver() database.PromptRunOwner {
	GinkgoHelper()
	cmd := exec.Command("true")
	Expect(cmd.Start()).To(Succeed())
	pid := int64(cmd.Process.Pid)
	Expect(cmd.Wait()).To(Succeed())
	return database.PromptRunOwner{HostID: database.LocalHostID(), PID: pid, StartedAt: time.Now().Add(-time.Minute), Token: uuid.New()}
}

var _ = Describe("reclaiming a run whose driver is gone", func() {
	var (
		db       *database.DB
		resolved api.ResolvedSpec
		sessions []database.CreateSessionInput
	)

	BeforeEach(func() {
		db = openRecordDB()
		resolved = api.ResolvedSpec{Spec: api.Spec{
			Model:  api.Model{Name: "claude-sonnet-5", Provider: api.Anthropic, Mode: api.ModeAgent},
			Prompt: api.Prompt{User: "reply to the comment thread"},
		}}
		sessions = hostTree()
	})

	// admit files a run for a fresh delivery on the host's session; the first
	// creates the session tree, later ones are admitted on it.
	admit := func(ctx SpecContext) (*database.PromptRun, error) {
		placement := promptrun.Placement{Sessions: sessions}
		return promptrun.Admit(ctx, promptrun.Input{Resolved: resolved, Record: &promptrun.Recording{
			DB: db, Placement: placement, Origin: "gavel.comments", AdmissionKey: "delivery-" + uuid.NewString(),
		}})
	}
	driveBy := func(ctx SpecContext, run *database.PromptRun, owner database.PromptRunOwner) {
		GinkgoHelper()
		_, err := db.ClaimPromptRunOwner(ctx, run.ID, owner, nil)
		Expect(err).NotTo(HaveOccurred())
	}
	reload := func(ctx SpecContext, run *database.PromptRun) *database.PromptRun {
		GinkgoHelper()
		stored, err := db.GetPromptRun(ctx, run.ID)
		Expect(err).NotTo(HaveOccurred())
		return stored
	}

	It("claims an admitted run for the admitting process", func(ctx SpecContext) {
		self, err := runowner.Local()
		Expect(err).NotTo(HaveOccurred())

		run, err := admit(ctx)

		Expect(err).NotTo(HaveOccurred())
		Expect(run.Owner).NotTo(BeNil())
		owner := *run.Owner
		Expect(owner.HeartbeatAt).NotTo(BeNil())
		owner.HeartbeatAt = nil
		Expect(owner).To(Equal(database.PromptRunOwner{HostID: self.HostID, PID: self.PID, StartedAt: self.StartedAt, Token: self.Token}))
	})

	It("cancels the orphaned run holding the session, with its approvals, and admits the new one", func(ctx SpecContext) {
		orphan, err := admit(ctx)
		Expect(err).NotTo(HaveOccurred())
		dead := exitedDriver()
		driveBy(ctx, orphan, dead)
		approval, err := db.CreateToolApprovalRequest(ctx, database.CreateToolApprovalRequestInput{
			SessionID: orphan.SessionID, PromptRunID: orphan.ID, ToolCallID: "call-1", Tool: "Bash",
			Input: map[string]any{"command": "make build"}, ExpiresAt: time.Now().Add(time.Hour),
		})
		Expect(err).NotTo(HaveOccurred())

		next, err := admit(ctx)

		Expect(err).NotTo(HaveOccurred())
		Expect(next.ID).NotTo(Equal(orphan.ID))
		Expect(next.SessionID).To(Equal(orphan.SessionID))
		Expect(next.State).To(Equal(database.PromptRunStatePending))
		reclaimed := reload(ctx, orphan)
		Expect(reclaimed.State).To(Equal(database.PromptRunStateCancelled))
		Expect(reclaimed.Phase).To(Equal(database.PromptRunPhaseGenerate))
		Expect(reclaimed.Error).To(Equal("reclaimed: driver " + dead.Describe() + " has exited"))
		requests, err := db.ListTurnRequests(ctx, database.TurnRequestFilter{SessionID: orphan.SessionID, PromptRunID: &orphan.ID})
		Expect(err).NotTo(HaveOccurred())
		Expect(requests).To(HaveLen(1))
		Expect(requests[0].ID).To(Equal(approval.ID))
		Expect(requests[0].State).To(Equal(database.TurnRequestStateCancelled))
		held, err := db.TouchPromptRunOwner(ctx, orphan.ID, dead.Token)
		Expect(err).NotTo(HaveOccurred())
		Expect(held).To(BeFalse(), "a reclaimed run's heartbeat stops")
	})

	It("still refuses a second run while the session's driver is running", func(ctx SpecContext) {
		live, err := admit(ctx)
		Expect(err).NotTo(HaveOccurred())

		_, err = admit(ctx)

		var conflict *database.ActiveRunConflictError
		Expect(errors.As(err, &conflict)).To(BeTrue(), "got %v", err)
		Expect(err).To(MatchError(database.ErrPromptRunConflict))
		Expect(conflict.Run.ID).To(Equal(live.ID))
		Expect(reload(ctx, live).State).To(Equal(database.PromptRunStatePending))
	})

	It("refuses a replayed admission of a run another live process drives, leaving its owner in place", func(ctx SpecContext) {
		runID := uuid.New()
		replay := func() (*database.PromptRun, error) {
			return promptrun.Admit(ctx, promptrun.Input{Resolved: resolved, Record: &promptrun.Recording{
				DB: db, Placement: promptrun.Placement{Sessions: sessions}, Origin: "gavel.comments",
				PromptRunID: runID, AdmissionKey: "delivery-" + runID.String(),
			}})
		}
		first, err := replay()
		Expect(err).NotTo(HaveOccurred())
		self, err := runowner.Local()
		Expect(err).NotTo(HaveOccurred())
		rival := self
		rival.Token = uuid.New()
		driveBy(ctx, first, rival)

		_, err = replay()

		Expect(err).To(MatchError(database.ErrPromptRunDriven))
		Expect(err).To(MatchError(database.ErrPromptRunConflict))
		Expect(reload(ctx, first).Owner).To(HaveField("Token", rival.Token))
	})

	It("never reclaims a run parked on an answer, whatever became of its driver", func(ctx SpecContext) {
		parked, err := admit(ctx)
		Expect(err).NotTo(HaveOccurred())
		waiting := database.PromptRunStateWaiting
		_, err = db.UpdatePromptRun(ctx, database.UpdatePromptRunInput{ID: parked.ID, ExpectedVersion: parked.Version, State: &waiting})
		Expect(err).NotTo(HaveOccurred())
		driveBy(ctx, parked, exitedDriver())

		_, err = admit(ctx)

		Expect(err).To(MatchError(database.ErrPromptRunConflict))
		Expect(reload(ctx, parked).State).To(Equal(database.PromptRunStateWaiting))
	})

	It("sweeps every orphaned run and leaves driven ones alone", func(ctx SpecContext) {
		orphan, err := admit(ctx)
		Expect(err).NotTo(HaveOccurred())
		driveBy(ctx, orphan, exitedDriver())
		sessions = hostTree()
		live, err := admit(ctx)
		Expect(err).NotTo(HaveOccurred())

		reclaimed, err := runowner.ReclaimOrphans(ctx, db)

		Expect(err).NotTo(HaveOccurred())
		Expect(reclaimed).To(Equal(1))
		Expect(reload(ctx, orphan).State).To(Equal(database.PromptRunStateCancelled))
		Expect(reload(ctx, live).State).To(Equal(database.PromptRunStatePending))

		again, err := runowner.ReclaimOrphans(ctx, db)

		Expect(err).NotTo(HaveOccurred())
		Expect(again).To(BeZero(), "a reclaimed run is settled and not reclaimed twice")
	})
})
