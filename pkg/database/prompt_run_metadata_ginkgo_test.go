package database

import (
	"github.com/flanksource/commons-db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func openPromptRunMetadataDB(ctx SpecContext) *DB {
	GinkgoHelper()
	handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_prompt_run_metadata"})
	db, err := Open(ctx, WithDSN(handle.DSN()), WithMigrations())
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
	return db
}

var _ = Describe("prompt run metadata", func() {
	It("stores the admission metadata and merges later updates key by key", func(ctx SpecContext) {
		db := openPromptRunMetadataDB(ctx)
		session, err := db.CreateOrGetSession(ctx, CreateSessionInput{Source: "captain", Provider: "anthropic"})
		Expect(err).NotTo(HaveOccurred())

		trace := []any{map[string]any{"name": "request", "scope": "user"}}
		run, err := db.CreatePromptRun(ctx, CreatePromptRunInput{
			SessionID: session.ID,
			Metadata:  map[string]any{"specTrace": trace, "specWarnings": []any{"model defaulted"}},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(run.Metadata).To(Equal(map[string]any{"specTrace": trace, "specWarnings": []any{"model defaulted"}}))

		merged := map[string]any{"specWarnings": []any{"replaced"}, "runtimeProfile": map[string]any{"profile": "fast"}}
		updated, err := db.UpdatePromptRun(ctx, UpdatePromptRunInput{ID: run.ID, ExpectedVersion: run.Version, Metadata: &merged})
		Expect(err).NotTo(HaveOccurred())
		Expect(updated.Metadata).To(Equal(map[string]any{
			"specTrace":      trace,
			"specWarnings":   []any{"replaced"},
			"runtimeProfile": map[string]any{"profile": "fast"},
		}))

		reread, err := db.GetPromptRun(ctx, run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(reread.Metadata).To(Equal(updated.Metadata))
	})

	It("defaults a run without metadata to an empty object", func(ctx SpecContext) {
		db := openPromptRunMetadataDB(ctx)
		session, err := db.CreateOrGetSession(ctx, CreateSessionInput{Source: "captain", Provider: "openai"})
		Expect(err).NotTo(HaveOccurred())
		run, err := db.CreatePromptRun(ctx, CreatePromptRunInput{SessionID: session.ID})
		Expect(err).NotTo(HaveOccurred())
		Expect(run.Metadata).To(BeEmpty())

		var stored string
		Expect(db.Gorm().Raw(`SELECT metadata::text FROM captain_prompt_runs WHERE id = ?`, run.ID).Scan(&stored).Error).To(Succeed())
		Expect(stored).To(Equal("{}"))
	})

	It("refuses a null metadata update", func(ctx SpecContext) {
		db := openPromptRunMetadataDB(ctx)
		session, err := db.CreateOrGetSession(ctx, CreateSessionInput{Source: "captain", Provider: "google"})
		Expect(err).NotTo(HaveOccurred())
		run, err := db.CreatePromptRun(ctx, CreatePromptRunInput{SessionID: session.ID})
		Expect(err).NotTo(HaveOccurred())

		var null map[string]any
		_, err = db.UpdatePromptRun(ctx, UpdatePromptRunInput{ID: run.ID, ExpectedVersion: run.Version, Metadata: &null})
		Expect(err).To(MatchError(ErrInvalidPromptRun))
	})
})
