package cli

import (
	"time"

	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/promptrun"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// answeredRun is a direct provider call that answered, as the CLI files it.
func answeredRun(rendered PromptRenderResult, runtime api.Model, providerSessionID, text string) promptrun.Completed {
	return promptrun.Completed{
		Resolved: promptResolution(rendered, rendered.Input),
		Runtime:  api.RuntimeOf(runtime.Provider, runtime.Mode), Model: runtime.Name, ProviderSessionID: providerSessionID,
		Outcome: promptrun.Outcome{State: database.PromptRunStateSucceeded, Phase: database.PromptRunPhaseFinished, Text: text},
	}
}

func openCLIRecordDB() *database.DB {
	GinkgoHelper()
	handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_cli_prompt_record"})
	db, err := database.Open(GinkgoT().Context(), database.WithDSN(handle.DSN()), database.WithMigrations())
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() {
		setCaptainDBForTest(nil)
		Expect(db.Close()).To(Succeed())
	})
	setCaptainDBForTest(db)
	return db
}

func runsOn(db *database.DB, sessionID uuid.UUID) []database.PromptRun {
	GinkgoHelper()
	runs, err := db.ListPromptRuns(GinkgoT().Context(), database.PromptRunFilter{SessionID: &sessionID})
	Expect(err).NotTo(HaveOccurred())
	return runs
}

var _ = Describe("recording a captain prompt run", func() {
	var (
		db       *database.DB
		rendered PromptRenderResult
		agentRun api.Model
	)

	BeforeEach(func() {
		db = openCLIRecordDB()
		agentRun = api.Model{Name: "claude-sonnet-5", Provider: api.Anthropic, Mode: api.ModeAgent}
		rendered = PromptRenderResult{Name: "fix-bug", Model: agentRun.Name, Provider: "anthropic", Mode: "agent"}
		rendered.Input.Prompt.User = "fix the failing test"
		rendered.Resolution = api.ResolvedSpec{
			Trace:    []api.SpecLayer{api.RequestSpecLayer("request", api.Spec{Prompt: api.Prompt{User: "fix the failing test"}})},
			Warnings: []string{"no preset selected"},
		}
	})

	It("files a fresh run on a captain session of its own with the plain spec and its resolution", func(ctx SpecContext) {
		providerSessionID := uuid.NewString()
		Expect(recordCompletedRun(ctx, promptRecordingInput{Rendered: rendered, RunID: "run-1"},
			answeredRun(rendered, agentRun, providerSessionID, "done"))).To(Succeed())

		transcript, err := db.GetSessionByIdentity(ctx, providerSessionID, "claude", "", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(transcript.ParentSessionID).NotTo(BeNil(), "the transcript hangs under the captain admission session")
		admission, err := db.GetSession(ctx, *transcript.ParentSessionID)
		Expect(err).NotTo(HaveOccurred())
		Expect(admission.Source).To(Equal("captain"))
		Expect(admission.Title).To(Equal("fix-bug"))

		runs := runsOn(db, admission.ID)
		Expect(runs).To(HaveLen(1))
		run := runs[0]
		Expect(run.State).To(Equal(database.PromptRunStateSucceeded))
		Expect(run.ResultText).To(Equal("done"))
		Expect(run.Origin).To(Equal("captain"))
		Expect(run.AdmissionKey).To(Equal("run-1"))
		Expect(run.PromptMarkdown).To(Equal("fix the failing test"))
		Expect(run.ExecutionSessionID).To(Equal(&transcript.ID))
		Expect(run.RenderedSpec).To(HaveKeyWithValue("prompt", map[string]any{"user": "fix the failing test"}))
		Expect(run.RenderedSpec).NotTo(HaveKey("input"))
		Expect(run.Metadata).To(HaveKey("specTrace"))
		Expect(run.Metadata).To(HaveKeyWithValue("specWarnings", []any{"no preset selected"}))
		Expect(run.Runtime.Mode).To(Equal("run"))
		Expect(run.Runtime.Resolved).To(Equal(database.PromptRunRuntimeSelection{
			Provider: "anthropic", Mode: "agent", Model: "claude-sonnet-5",
		}))

		result, err := RunSessionGet(ctx, SessionGetOptions{ID: admission.ID.String()})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Sessions).To(ContainElement(And(
			HaveField("CaptainID", admission.ID.String()),
			HaveField("Detail.PromptRunMetadata", HaveKey("specTrace")),
		)), "the session aggregate exposes how the spec was resolved")
	})

	It("files a continuation on the transcript session of the provider session it resumes", func(ctx SpecContext) {
		providerSessionID := uuid.NewString()
		resumed, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: providerSessionID, Source: "claude", Provider: "anthropic", HostID: captainHostID(),
		})
		Expect(err).NotTo(HaveOccurred())
		rendered.Input.SessionID = providerSessionID

		Expect(recordCompletedRun(ctx, promptRecordingInput{Rendered: rendered, RunID: "resume-1"},
			answeredRun(rendered, agentRun, providerSessionID, "continued"))).To(Succeed())

		runs := runsOn(db, resumed.ID)
		Expect(runs).To(HaveLen(1))
		Expect(runs[0].ExecutionSessionID).To(Equal(&resumed.ID), "the continuation ran in the transcript it was admitted on")
	})

	It("files every iteration and a cancelled state for a stopped run", func(ctx SpecContext) {
		base := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
		verdicts := []agent.VerifyResult{{Valid: false, Iteration: 1, Report: verdictReport(1, false)}}
		done := answeredRun(rendered, agentRun, uuid.NewString(), "")
		done.Outcome = promptrun.Outcome{State: database.PromptRunStateCancelled, Phase: database.PromptRunPhaseFinished, Error: "stopped"}
		done.Iterations = promptrun.IterationRecords(promptrun.Result{Loop: loopWith(2, base, nil), Verdicts: verdicts}, true)
		sessionID := uuid.New()

		Expect(recordCompletedRun(ctx, promptRecordingInput{Rendered: rendered, RunID: "stopped", SessionID: sessionID}, done)).To(Succeed())

		runs := runsOn(db, sessionID)
		Expect(runs).To(HaveLen(1))
		Expect(runs[0].State).To(Equal(database.PromptRunStateCancelled))
		Expect(runs[0].Error).To(Equal("stopped"))
		iterations, err := db.ListPromptRunIterations(ctx, runs[0].ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(iterations).To(HaveLen(2))
		Expect(iterations[0].State).To(Equal(database.PromptRunIterationStateFailed))
		Expect(iterations[1].State).To(Equal(database.PromptRunIterationStateCancelled))
	})

	It("keeps the run and its good turns when one iteration is refused, and says so", func(ctx SpecContext) {
		base := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
		corrupt := verdictReport(2, true)
		corrupt.State = api.VerifyStateFailed // passed=true with a failed state: Validate rejects it
		verdicts := []agent.VerifyResult{
			{Valid: false, Iteration: 1, Report: verdictReport(1, false)},
			{Valid: true, Iteration: 2, Report: corrupt},
		}
		done := answeredRun(rendered, agentRun, uuid.NewString(), "fixed")
		done.Iterations = promptrun.IterationRecords(promptrun.Result{Loop: loopWith(2, base, nil), Verdicts: verdicts}, false)
		sessionID := uuid.New()

		err := recordCompletedRun(ctx, promptRecordingInput{Rendered: rendered, RunID: "partial", SessionID: sessionID}, done)
		Expect(err).To(MatchError(ContainSubstring("iteration 2")))

		runs := runsOn(db, sessionID)
		Expect(runs).To(HaveLen(1), "the run row survives a refused iteration")
		Expect(runs[0].State).To(Equal(database.PromptRunStateSucceeded))
		iterations, listErr := db.ListPromptRunIterations(ctx, runs[0].ID)
		Expect(listErr).NotTo(HaveOccurred())
		Expect(iterations).To(HaveLen(1))
		Expect(iterations[0].Iteration).To(Equal(1))
	})

	It("records a verify-only workflow run through promptrun.Run", func(ctx SpecContext) {
		workflow := workflowRendered(&api.Verify{Commands: []string{"true"}})
		workflow.Input.SetCwd(GinkgoT().TempDir())
		workflow.Resolution = rendered.Resolution

		result, err := executeSyncRunSingle(ctx, workflow, AIPromptOptions{})
		Expect(err).NotTo(HaveOccurred())

		runs, err := db.ListPromptRuns(ctx, database.PromptRunFilter{})
		Expect(err).NotTo(HaveOccurred())
		Expect(runs).To(ContainElement(And(
			HaveField("AdmissionKey", result.RunID),
			HaveField("State", database.PromptRunStateSucceeded),
			HaveField("Phase", database.PromptRunPhaseFinished),
			HaveField("Metadata", HaveKey("specTrace")),
		)))
	})
})
