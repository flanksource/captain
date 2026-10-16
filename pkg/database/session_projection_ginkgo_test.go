package database

import (
	"time"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Re-ingesting a transcript is the common case: every append re-derives the
// whole file. The projected captain_sessions columns are almost always already
// current, and rewriting them anyway mints a new row version (and new TOAST
// chunks for metadata) each time.
var _ = Describe("transcript session projection", func() {
	It("leaves the session row version untouched when an identical batch is re-ingested", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_session_projection"})
		db, err := Open(ctx, WithDSN(handle.DSN()), WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })

		batch := testIngestBatch(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC), 4096)
		// A model call without timestamps is stamped with its write time by
		// captain_set_model_call_state, which advances last_activity_at on every
		// upsert; pin the calls to their turns so only the projection is measured.
		for i := range batch.Turns {
			batch.Turns[i].Call.StartedAt, batch.Turns[i].Call.EndedAt = batch.Turns[i].StartedAt, batch.Turns[i].EndedAt
		}
		session, err := db.IngestTranscript(ctx, batch)
		Expect(err).NotTo(HaveOccurred())
		rowVersion := func(id uuid.UUID) string {
			GinkgoHelper()
			var xmin string
			Expect(db.Gorm().Raw(`SELECT xmin::text FROM captain_sessions WHERE id = ?`, id).Scan(&xmin).Error).To(Succeed())
			Expect(xmin).NotTo(BeEmpty())
			return xmin
		}
		before := rowVersion(session.ID)

		_, err = db.IngestTranscript(ctx, batch)
		Expect(err).NotTo(HaveOccurred())
		Expect(rowVersion(session.ID)).To(Equal(before), "an identical re-ingest must issue no captain_sessions UPDATE")

		const renamed = "Renamed fixture session"
		batch.Session.Title = renamed
		batch.Session.Metadata = map[string]any{"providerName": "Claude Code", "entrypoint": "cli"}
		_, err = db.IngestTranscript(ctx, batch)
		Expect(err).NotTo(HaveOccurred())
		Expect(rowVersion(session.ID)).NotTo(Equal(before), "a changed projection must still be written")
		reread, err := db.GetSession(ctx, session.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(reread.Title).To(Equal(renamed))
		Expect(reread.Metadata).To(HaveKeyWithValue("entrypoint", "cli"))
	})
})
