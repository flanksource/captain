package cli

import (
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/monitor"
	"github.com/flanksource/captain/pkg/plans"
	"github.com/flanksource/commons-db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("read-only plan lookup", Serial, func() {
	const planMarkdown = "# Persisted plan\n\n- keep reads read-only"

	var (
		db         *database.DB
		session    *database.Session
		saved      plans.Outcome
		discovered int
	)

	BeforeEach(func(ctx SpecContext) {
		databaseURLs = nil
		databaseContextFlagValue = ""
		GinkgoT().Setenv("HOME", GinkgoT().TempDir())
		GinkgoT().Setenv(databaseContextEnv, "")
		GinkgoT().Setenv(databaseContextsEnv, "")
		resetCaptainContextsForTest()

		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_plan_readonly"})
		var err error
		db, err = database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
		setCaptainContextDBForTest(testDatabaseHandle{Name: defaultDatabaseContextName, DB: db})

		session, err = db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: "readonly-plan-session", Source: "codex", Provider: "openai",
		})
		Expect(err).NotTo(HaveOccurred())
		saved, err = plans.Save(ctx, db, plans.SaveInput{
			Plan:     database.CreatePlanInput{SourceSessionID: session.ID, Variant: "primary"},
			Markdown: planMarkdown,
		}, nil)
		Expect(err).NotTo(HaveOccurred())

		discovered = 0
		monitorDiscoverProcesses = func() ([]monitor.Process, error) {
			discovered++
			return nil, nil
		}
		DeferCleanup(func() {
			monitorDiscoverProcesses = nil
			resetCaptainContextsForTest()
		})
	})

	It("resolves a session's persisted plan without a monitor pass", func(ctx SpecContext) {
		result, err := ResolvePlan(ctx, db, PlanOptions{SessionID: session.ID.String(), Source: "all"})

		Expect(err).NotTo(HaveOccurred())
		Expect(result.PlanID).To(Equal(saved.Plan.ID.String()))
		Expect(result.RevisionID).To(Equal(saved.Revision.ID.String()))
		Expect(result.Content).To(Equal(planMarkdown))
		Expect(discovered).To(BeZero(), "a plan read must never ingest transcripts or poll processes")
	})

	It("keeps the captain CLI's freshening pass in RunPlan", func() {
		result, err := RunPlan(PlanOptions{SessionID: session.ID.String(), Source: "all"})

		Expect(err).NotTo(HaveOccurred())
		Expect(result.Content).To(Equal(planMarkdown))
		Expect(discovered).To(BeNumerically(">", 0))
	})

	It("reports ErrNoPlan for a session that recorded none", func(ctx SpecContext) {
		bare, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: "readonly-no-plan-session", Source: "codex", Provider: "openai",
		})
		Expect(err).NotTo(HaveOccurred())

		_, err = ResolvePlan(ctx, db, PlanOptions{SessionID: bare.ID.String(), Source: "all"})

		Expect(err).To(MatchError(ErrNoPlan))
	})

	It("refuses a missing database handle", func(ctx SpecContext) {
		_, err := ResolvePlan(ctx, nil, PlanOptions{SessionID: session.ID.String(), Source: "all"})

		Expect(err).To(MatchError(ContainSubstring("database")))
	})
})
