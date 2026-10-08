package query

import (
	"context"
	"time"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Follow facets", Ordered, func() {
	var db *database.DB

	BeforeAll(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_session_follow_facets"})
		var err error
		db, err = database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
	})

	newSession := func(ctx context.Context) *database.Session {
		GinkgoHelper()
		created, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: uuid.NewString(), Source: "claude", Provider: "anthropic", HostID: "follow-facets-test",
		})
		Expect(err).NotTo(HaveOccurred())
		return created
	}
	follow := func(ctx context.Context, id uuid.UUID) <-chan FollowEvent {
		GinkgoHelper()
		followCtx, cancel := context.WithCancel(ctx)
		events, err := Follow(followCtx, db, id.String(), FollowOptions{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			cancel()
			Eventually(events, followWait).Should(BeClosed())
		})
		return events
	}
	exec := func(ctx context.Context, statement string, args ...any) {
		GinkgoHelper()
		Expect(db.Gorm().WithContext(ctx).Exec(statement, args...).Error).To(Succeed())
	}
	// expectFacetsChange asserts the next frame is a state frame that differs
	// from previous only in its facets fingerprint, and returns it.
	expectFacetsChange := func(events <-chan FollowEvent, previous FollowState) FollowState {
		GinkgoHelper()
		event := nextEvent(events)
		Expect(event.Message).To(BeNil(), "a facet change is not a message")
		Expect(event.State).NotTo(BeNil())
		Expect(event.State.Facets).NotTo(Equal(previous.Facets))
		Expect(withoutFacets(*event.State)).To(Equal(withoutFacets(previous)))
		return *event.State
	}

	It("emits a state frame when a native plan is created, revised and approved", func(ctx SpecContext) {
		followed := newSession(ctx)
		events := follow(ctx, followed.ID)
		initial := nextState(events)
		Expect(initial.Facets).NotTo(BeEmpty())

		plan, err := db.CreateOrGetPlan(ctx, database.CreatePlanInput{SourceSessionID: followed.ID, Title: "draft"})
		Expect(err).NotTo(HaveOccurred())
		created := expectFacetsChange(events, initial)

		revision, err := db.AppendPlanRevision(ctx, database.AppendPlanRevisionInput{PlanID: plan.ID, PlanMarkdown: "# step one"})
		Expect(err).NotTo(HaveOccurred())
		revised := expectFacetsChange(events, created)

		_, err = db.ApprovePlanRevision(ctx, database.ApprovePlanRevisionInput{PlanID: plan.ID, RevisionID: revision.ID})
		Expect(err).NotTo(HaveOccurred())
		expectFacetsChange(events, revised)
		expectNoMessage(events)
	})

	It("emits a state frame when the session metadata plan changes", func(ctx SpecContext) {
		followed := newSession(ctx)
		events := follow(ctx, followed.ID)
		initial := nextState(events)

		exec(ctx, `UPDATE captain_sessions SET metadata = metadata || '{"plan":{"content":"step one"}}'::jsonb WHERE id = ?`, followed.ID)
		expectFacetsChange(events, initial)
		expectNoMessage(events)
	})

	It("emits a state frame when an approval request is answered", func(ctx SpecContext) {
		followed := newSession(ctx)
		var requestID string
		Expect(db.Gorm().WithContext(ctx).Raw(
			`INSERT INTO captain_turn_requests (session_id, kind) VALUES (?, 'question') RETURNING id`, followed.ID).
			Scan(&requestID).Error).To(Succeed())
		events := follow(ctx, followed.ID)
		initial := nextState(events)

		exec(ctx, `UPDATE captain_turn_requests SET state = 'answered', response = '{"answer":"yes"}'::jsonb WHERE id = ?`, requestID)
		expectFacetsChange(events, initial)
	})

	It("emits nothing for a wake that changes no facet", func(ctx SpecContext) {
		followed := newSession(ctx)
		events := follow(ctx, followed.ID)
		nextState(events)

		exec(ctx, `SELECT pg_notify(?, ?)`, SessionChangeChannel, followed.ID.String())
		select {
		case event := <-events:
			Fail("unexpected follow event after a no-op wake: " + describeEvent(event))
		case <-time.After(followQuiet):
		}
	})

	It("closes a follower of a run's execution session once the run succeeds", func(ctx SpecContext) {
		providerSessionID := uuid.NewString()
		admission, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: providerSessionID, Source: "gavel", HostID: "follow-facets-test",
		})
		Expect(err).NotTo(HaveOccurred())
		execution, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: providerSessionID, Source: "claude", Provider: "anthropic", HostID: "follow-facets-test",
			ParentSessionID: &admission.ID, ParentRelation: database.SessionParentRelationTranscript,
		})
		Expect(err).NotTo(HaveOccurred())
		run, err := db.CreatePromptRun(ctx, database.CreatePromptRunInput{SessionID: admission.ID, ExecutionSessionID: &execution.ID})
		Expect(err).NotTo(HaveOccurred())
		events := follow(ctx, execution.ID)
		Expect(nextState(events).LifecycleStatus).To(Equal("created"))

		running := database.PromptRunStateRunning
		run, err = db.UpdatePromptRun(ctx, database.UpdatePromptRunInput{ID: run.ID, ExpectedVersion: run.Version, State: &running})
		Expect(err).NotTo(HaveOccurred())
		Expect(nextState(events).LifecycleStatus).To(Equal("running"))

		succeeded, finished := database.PromptRunStateSucceeded, database.PromptRunPhaseFinished
		_, err = db.UpdatePromptRun(ctx, database.UpdatePromptRunInput{
			ID: run.ID, ExpectedVersion: run.Version, State: &succeeded, Phase: &finished,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(nextState(events).LifecycleStatus).To(Equal("succeeded"))
		Eventually(events, followWait).Should(BeClosed())
	})
})

func withoutFacets(state FollowState) FollowState {
	state.Facets = ""
	return state
}

func describeEvent(event FollowEvent) string {
	switch {
	case event.Message != nil:
		return "message " + event.Message.ID
	case event.State != nil:
		return "state " + event.State.LifecycleStatus + " facets " + event.State.Facets
	case event.Err != nil:
		return "error " + event.Err.Error()
	}
	return "stream closed"
}
