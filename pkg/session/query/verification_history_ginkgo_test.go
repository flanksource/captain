package query

import (
	"encoding/json"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Verification history", func() {
	It("preserves inconsistent reports in single reads, batches and composed sessions", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_verification_history"})
		db, err := database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })

		created, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: uuid.NewString(), Source: "codex", Provider: "openai",
		})
		Expect(err).NotTo(HaveOccurred())
		run, err := db.CreatePromptRun(ctx, database.CreatePromptRunInput{SessionID: created.ID})
		Expect(err).NotTo(HaveOccurred())
		summary := api.VerifySummary{Total: 617, Passed: 616, Skipped: 1}
		historical := api.VerifyReport{
			Kind: api.VerifyKindFixture, Name: "fixture", Ran: true, Iteration: 1,
			State: api.VerifyStateSkipped, Summary: summary,
			Tests: []api.VerifyNode{{Name: "test suite", Summary: &summary}},
		}
		Expect(historical.Validate()).To(MatchError(ContainSubstring(`state "skipped" but its tests are "passed"`)))
		raw, err := json.Marshal(historical)
		Expect(err).NotTo(HaveOccurred())
		Expect(db.Gorm().Exec(`INSERT INTO captain_prompt_run_iterations
			(prompt_run_id, iteration, state, verification_result) VALUES (?, 1, 'failed', ?::jsonb)`,
			run.ID, string(raw)).Error).To(Succeed())

		report, iteration, err := db.LatestPromptRunVerification(ctx, run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(iteration).To(Equal(1))
		Expect(report).To(Equal(&historical))

		validSession, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: uuid.NewString(), Source: "codex", Provider: "openai",
		})
		Expect(err).NotTo(HaveOccurred())
		validRun, err := db.CreatePromptRun(ctx, database.CreatePromptRunInput{SessionID: validSession.ID})
		Expect(err).NotTo(HaveOccurred())
		valid := api.NewNodeReport(api.VerifyKindFixture, "valid", api.VerifyNode{Name: "check", Passed: true})
		_, err = db.UpsertPromptRunIteration(ctx, database.UpsertPromptRunIterationInput{
			PromptRunID: validRun.ID, Iteration: 1, State: database.PromptRunIterationStateSucceeded,
			VerificationResult: &valid,
		})
		Expect(err).NotTo(HaveOccurred())
		batch, err := db.LatestPromptRunVerifications(ctx, []uuid.UUID{run.ID, validRun.ID})
		Expect(err).NotTo(HaveOccurred())
		Expect(batch).To(Equal(map[uuid.UUID]database.PromptRunVerification{
			run.ID: {Iteration: 1, Report: &historical}, validRun.ID: {Iteration: 1, Report: &valid},
		}))

		overview, err := db.GetSessionOverviewByIdentity(ctx, created.ID.String())
		Expect(err).NotTo(HaveOccurred())
		result, failures, err := ComposeSession(ctx, db, *overview, ComposeOptions{
			Transcript: &session.Session{Messages: []session.Message{
				{ID: "transcript", Role: "user", Parts: []session.Part{{Type: session.PartText, Text: "original message"}}},
			}},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(failures).To(BeEmpty())
		Expect(result.Session.Verifications).To(Equal([]session.Verification{{Iteration: 1, Report: historical}}))
		Expect(result.Session.Messages).To(ContainElement(HaveField("ID", "transcript")))
		iterations, err := db.ListPromptRunIterations(ctx, run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(iterations[0].VerificationResult).To(Equal(&historical))
	})
})
