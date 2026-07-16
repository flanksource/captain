package cli

import (
	"os"
	"path/filepath"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	commonsdb "github.com/flanksource/commons-db/db"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("prompt batch sessions", func() {
	It("creates the batch ID as the canonical root with one child per runtime", func() {
		if os.Getenv("CAPTAIN_DB_EMBEDDED_TEST") == "" {
			Skip("set CAPTAIN_DB_EMBEDDED_TEST=1 to run embedded-postgres cli tests")
		}
		dsn, stop, err := commonsdb.StartEmbedded(commonsdb.EmbeddedConfig{
			DataDir: filepath.Join(GinkgoT().TempDir(), "postgres"), Database: "captain_prompt_batch",
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(stop()).To(Succeed()) })
		db, err := database.Open(GinkgoT().Context(), database.Config{DSN: dsn})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			setCaptainDBForTest(nil)
			Expect(db.Close()).To(Succeed())
		})
		setCaptainDBForTest(db)

		rendered := PromptRenderResult{Name: "compare", Backend: "codex-cmux", Model: "gpt-5.6-sol"}
		rendered.Input.Prompt.User = "Compare these approaches"
		rendered.Input.SetCwd("/workspace/captain")
		batch, err := createPromptBatchSessions(GinkgoT().Context(), rendered, []api.Model{
			{Name: "gpt-5.6-sol", Backend: api.BackendCodexCmux, Effort: api.EffortHigh},
			{Name: "gemini-2.5-flash", Backend: api.BackendGemini},
		})
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
			Expect(child.ParentSessionID).NotTo(BeNil())
			Expect(*child.ParentSessionID).To(Equal(batch.ID))
			Expect(child.RootSessionID).NotTo(BeNil())
			Expect(*child.RootSessionID).To(Equal(batch.ID))
			Expect(child.ProviderSessionID).To(BeEmpty())
			Expect(child.Description).To(Equal(runtimeSelector(batch.Runs[i].Runtime)))
		}
	})
})
