package database

import (
	"time"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("named prompt runs", func() {
	It("returns one name with its latest run across consumers and omits unnamed runs", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_named_prompt_runs"})
		db, err := Open(ctx, WithDSN(handle.DSN()), WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
		empty, err := db.ListNamedPrompts(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(empty).To(Equal([]NamedPrompt{}))

		queued := time.Date(2026, time.October, 18, 9, 0, 0, 0, time.UTC)
		create := func(name, origin, sessionIdentity string, at time.Time) NamedPrompt {
			session, err := db.CreateOrGetSession(ctx, CreateSessionInput{Source: origin, ProviderSessionID: sessionIdentity})
			Expect(err).NotTo(HaveOccurred())
			run, err := db.CreatePromptRun(ctx, CreatePromptRunInput{ID: uuid.New(), SessionID: session.ID, Origin: origin, SpecProfile: name})
			Expect(err).NotTo(HaveOccurred())
			Expect(db.Gorm().WithContext(ctx).Exec("UPDATE captain_prompt_runs SET queued_at = ? WHERE id = ?", at, run.ID).Error).To(Succeed())
			return NamedPrompt{Name: name, Origin: origin, RunID: run.ID, SessionID: session.ID, QueuedAt: at}
		}
		create("review", "gavel.todos", "old-review", queued)
		latestReview := create("review", "captain", "new-review", queued.Add(time.Minute))
		beta := create("triage", "gavel.todos", "triage", queued.Add(2*time.Minute))
		create("", "aichat", "unnamed-chat", queued.Add(3*time.Minute))

		names, err := db.ListNamedPrompts(ctx)
		Expect(err).NotTo(HaveOccurred())
		for i := range names {
			names[i].QueuedAt = names[i].QueuedAt.UTC()
		}
		Expect(names).To(Equal([]NamedPrompt{latestReview, beta}))
	})
})
