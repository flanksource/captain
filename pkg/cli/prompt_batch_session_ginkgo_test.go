package cli

import (
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("prompt batch sessions", func() {
	It("creates the batch ID as the canonical root with one child per runtime", func() {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_prompt_batch"})
		db, err := database.Open(GinkgoT().Context(), database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			setCaptainDBForTest(nil)
			Expect(db.Close()).To(Succeed())
		})
		setCaptainDBForTest(db)

		rendered := PromptRenderResult{Name: "compare", Provider: "openai", Mode: "cmux", Model: "gpt-5.6-sol"}
		rendered.Input.Prompt.User = "Compare these approaches"
		rendered.Input.SetCwd("/workspace/captain")
		// The batch is handed resolved runtimes, exactly as prompt execution hands
		// it the output of resolvePromptRuntimes — a member with no provider is a
		// caller bug, and validatePromptRuntimes says so rather than deriving one.
		batch, err := createPromptBatchSessions(GinkgoT().Context(), rendered, resolveAll(
			api.Model{Name: "gpt-5.6-sol", Mode: api.ModeCmux, Effort: api.EffortHigh},
			api.Model{Name: "gemini-2.5-flash", Mode: api.ModeAPI},
		))
		Expect(err).NotTo(HaveOccurred())
		Expect(batch.ID).NotTo(Equal(uuid.Nil))
		Expect(batch.Runs).To(HaveLen(2))

		root, err := db.GetSession(GinkgoT().Context(), batch.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(root.ParentSessionID).To(BeNil())
		Expect(root.RootSessionID).To(BeNil())
		Expect(root.Source).To(Equal("captain"))
		Expect(root.Provider).To(Equal("multi-model"))
		Expect(root.AgentType).To(Equal("batch"))
		Expect(root.InitialPrompt).To(Equal("Compare these approaches"))

		thread, err := db.ListThreadSessionOverviews(GinkgoT().Context(), batch.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(thread).To(HaveLen(3))
		updatedRoot, err := db.UpdateSessionLifecycle(
			GinkgoT().Context(),
			batch.ID,
			database.SessionLifecyclePartial,
			"1 succeeded, 1 failed",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(updatedRoot.LifecycleStatus).To(Equal(database.SessionLifecyclePartial))
		Expect(updatedRoot.StateVersion).To(Equal(int64(1)))
		Expect(updatedRoot.EndedAt).NotTo(BeNil())
		result, err := RunSessionGet(GinkgoT().Context(), SessionGetOptions{ID: batch.ID.String()})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RootSessionID).To(Equal(batch.ID.String()))
		Expect(result.Sessions).To(HaveLen(3))
		Expect(result.Tree().GetChildren()).To(HaveLen(1))
		Expect(result.Tree().GetChildren()[0].GetChildren()).To(HaveLen(2))
		for i, run := range batch.Runs {
			Expect(run.SessionID).NotTo(Equal(uuid.Nil))
			child, err := db.GetSession(GinkgoT().Context(), run.SessionID)
			Expect(err).NotTo(HaveOccurred())
			Expect(child.Source).To(Equal("captain"))
			Expect(child.ParentSessionID).NotTo(BeNil())
			Expect(*child.ParentSessionID).To(Equal(batch.ID))
			Expect(child.RootSessionID).NotTo(BeNil())
			Expect(*child.RootSessionID).To(Equal(batch.ID))
			Expect(child.ProviderSessionID).To(BeEmpty())
			Expect(child.Description).To(Equal(runtimeSelector(batch.Runs[i].Runtime)))
		}
	})

	It("adopts a monitor-first transcript beneath its Captain admission session", func() {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_prompt_batch_monitor_first"})
		db, err := database.Open(GinkgoT().Context(), database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			setCaptainDBForTest(nil)
			Expect(db.Close()).To(Succeed())
		})
		setCaptainDBForTest(db)

		rendered := PromptRenderResult{Name: "compare", Provider: "openai", Mode: "cmux", Model: "gpt-5.6-sol"}
		rendered.Input.Prompt.User = "Compare these approaches"
		rendered.Input.SetCwd("/workspace/captain")
		batch, err := createPromptBatchSessions(GinkgoT().Context(), rendered, resolveAll(
			api.Model{Name: "gpt-5.6-sol", Mode: api.ModeCmux, Effort: api.EffortHigh},
			api.Model{Name: "gemini-2.5-flash", Mode: api.ModeAPI},
		))
		Expect(err).NotTo(HaveOccurred())

		local := batch.Runs[0]
		providerSessionID := "0195c1de-4ab8-7000-8000-00000000ba7c"
		observed, err := db.CreateOrGetSession(GinkgoT().Context(), database.CreateSessionInput{
			ProviderSessionID: providerSessionID,
			Source:            transcriptSource(local.Runtime.Provider, local.Runtime.Mode),
			Provider:          providerName(local.Runtime.Provider),
			HostID:            captainHostID(),
			CWD:               rendered.Input.Cwd(),
		})
		Expect(err).NotTo(HaveOccurred())

		persistPromptRun(GinkgoT().Context(), promptRunRecordInput{
			Rendered: rendered, RunID: "monitor-first-run", Binding: promptBinding(batch, 0),
			SessionID: providerSessionID, Model: local.Runtime.Name,
			Provider: local.Runtime.Provider, Mode: local.Runtime.Mode, ResultText: "done",
		})

		admission, err := db.GetSession(GinkgoT().Context(), local.SessionID)
		Expect(err).NotTo(HaveOccurred())
		Expect(admission.Source).To(Equal("captain"))
		Expect(admission.ProviderSessionID).To(Equal(providerSessionID))
		transcript, err := db.GetTranscriptSession(GinkgoT().Context(), admission.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(transcript.ID).To(Equal(observed.ID))
		Expect(transcript.Source).To(Equal("codex"))
		Expect(transcript.ParentRelation).To(Equal(database.SessionParentRelationTranscript))
		Expect(transcript.RootSessionID).To(Equal(&batch.ID))

		runs, err := db.ListPromptRuns(GinkgoT().Context(), database.PromptRunFilter{SessionID: &admission.ID})
		Expect(err).NotTo(HaveOccurred())
		Expect(runs).To(HaveLen(1))
		Expect(runs[0].ExecutionSessionID).To(Equal(&observed.ID))
		Expect(runs[0].BatchID).To(Equal(&batch.ID))
		Expect(runs[0].State).To(Equal(database.PromptRunStateSucceeded))

		apiRun := batch.Runs[1]
		persistPromptRun(GinkgoT().Context(), promptRunRecordInput{
			Rendered: rendered, RunID: "api-run", Binding: promptBinding(batch, 1),
			Model: apiRun.Runtime.Name, Provider: apiRun.Runtime.Provider,
			Mode: apiRun.Runtime.Mode, ResultText: "done",
		})
		apiRuns, err := db.ListPromptRuns(GinkgoT().Context(), database.PromptRunFilter{SessionID: &apiRun.SessionID})
		Expect(err).NotTo(HaveOccurred())
		Expect(apiRuns).To(HaveLen(1))
		Expect(apiRuns[0].ExecutionSessionID).To(BeNil())
	})
})
