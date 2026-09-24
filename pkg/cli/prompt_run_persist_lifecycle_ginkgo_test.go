package cli

import (
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The session lifecycle of a persisted run is projected by migration 84 from
// the run row alone, for the admission session and the transcript it executed
// in, exactly as it is for runs any other producer records.
var _ = Describe("persisted prompt run session lifecycle", func() {
	It("records an interrupted batch member as cancelled on its admission and transcript sessions", func() {
		ctx := GinkgoT().Context()
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_prompt_run_persist_lifecycle"})
		db, err := database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			setCaptainDBForTest(nil)
			Expect(db.Close()).To(Succeed())
		})
		setCaptainDBForTest(db)

		rendered := PromptRenderResult{Name: "compare", Provider: "anthropic", Mode: "agent", Model: "claude-sonnet-5"}
		rendered.Input.Prompt.User = "Compare these approaches"
		rendered.Input.SetCwd("/workspace/captain")
		batch, err := createPromptBatchSessions(ctx, rendered, resolveAll(
			api.Model{Name: "claude-sonnet-5", Mode: api.ModeAgent},
			api.Model{Name: "gemini-2.5-flash", Mode: api.ModeAPI},
		))
		Expect(err).NotTo(HaveOccurred())
		member := batch.Runs[0]
		const providerSessionID = "0195c1de-4ab8-7000-8000-0000000c0de1"

		persistPromptRun(ctx, promptRunRecordInput{
			Rendered: rendered, RunID: "interrupted-member", Binding: promptBinding(batch, 0),
			SessionID: providerSessionID, Model: member.Runtime.Name, Provider: member.Runtime.Provider,
			Mode: member.Runtime.Mode, State: database.PromptRunStateCancelled, Error: "interrupted by user",
		})

		admission, err := db.GetSession(ctx, member.SessionID)
		Expect(err).NotTo(HaveOccurred())
		transcript, err := db.GetTranscriptSession(ctx, admission.ID)
		Expect(err).NotTo(HaveOccurred())
		for _, projected := range []*database.Session{admission, transcript} {
			Expect(projected.LifecycleStatus).To(Equal(database.SessionLifecycleCancelled), projected.Source)
			Expect(projected.StateReason).To(Equal("interrupted by user"), projected.Source)
			Expect(projected.EndedAt).NotTo(BeNil(), projected.Source)
		}
	})
})
