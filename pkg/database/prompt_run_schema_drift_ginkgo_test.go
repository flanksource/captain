package database

import (
	"database/sql"

	"github.com/flanksource/commons-db/dbtest"
	_ "github.com/jackc/pgx/v5/stdlib"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Captain and its hosts share one database, so whichever process starts first
// migrates while the others keep their pools open. A column another process
// adds must not break a statement this pool already prepared.
var _ = Describe("a pool that outlives another process's migration", func() {
	It("keeps reading prompt runs after a column is added to captain_prompt_runs", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_prompt_run_schema_drift"})
		db, err := Open(ctx, WithDSN(handle.DSN()), WithMigrations(), WithMaxOpenConns(1))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })

		session, err := db.CreateOrGetSession(ctx, CreateSessionInput{Source: "captain", Provider: "anthropic"})
		Expect(err).NotTo(HaveOccurred())
		run, err := db.CreatePromptRun(ctx, CreatePromptRunInput{SessionID: session.ID})
		Expect(err).NotTo(HaveOccurred())
		_, err = db.GetPromptRun(ctx, run.ID)
		Expect(err).NotTo(HaveOccurred())

		migrator, err := sql.Open("pgx", handle.DSN())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(migrator.Close()).To(Succeed()) })
		_, err = migrator.ExecContext(ctx, `ALTER TABLE captain_prompt_runs ADD COLUMN schema_drift_probe text`)
		Expect(err).NotTo(HaveOccurred())

		reread, err := db.GetPromptRun(ctx, run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(reread.ID).To(Equal(run.ID))
	})
})
