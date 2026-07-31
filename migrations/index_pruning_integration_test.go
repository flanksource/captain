package migrations

import (
	"github.com/flanksource/commons-db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Removing an index from the HCL only prunes a real database if Atlas treats it
// as drift to reconcile rather than as an unmanaged object to leave alone. The
// indexes this schema deliberately no longer declares were measured dead, but
// they sit on the two most write-heavy tables, so leaving them behind on an
// existing database would keep paying their upkeep forever.
var _ = Describe("Captain index reconciliation", func() {
	It("prunes indexes the schema no longer declares and narrows the ones it redeclares", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_index_pruning"})
		dsn, db := handle.DSN(), handle.SQL()

		Expect(Apply(ctx, dsn)).To(Succeed())

		// Reproduce what a database migrated by an earlier Captain looks like:
		// the dead indexes present, and model_call_id indexed in full rather
		// than only where it is set.
		for _, statement := range []string{
			`CREATE INDEX captain_sessions_project_idx ON public.captain_sessions (project)`,
			`CREATE INDEX captain_model_calls_started_at_idx ON public.captain_model_calls (started_at)`,
			`CREATE INDEX captain_model_calls_model_idx ON public.captain_model_calls (model, backend)`,
			`CREATE INDEX captain_model_calls_iteration_id_idx ON public.captain_model_calls (iteration_id)`,
			`DROP INDEX public.captain_messages_model_call_id_idx`,
			`CREATE INDEX captain_messages_model_call_id_idx ON public.captain_messages (model_call_id)`,
		} {
			_, err := db.ExecContext(ctx, statement)
			Expect(err).NotTo(HaveOccurred(), statement)
		}

		Expect(Apply(ctx, dsn)).To(Succeed())

		indexDefinition := func(name string) string {
			var definition *string
			Expect(db.QueryRowContext(ctx,
				`SELECT indexdef FROM pg_indexes WHERE schemaname = 'public' AND indexname = $1`, name).
				Scan(&definition)).To(Or(Succeed(), MatchError(ContainSubstring("no rows"))))
			if definition == nil {
				return ""
			}
			return *definition
		}

		for _, dropped := range []string{
			"captain_sessions_project_idx",
			"captain_model_calls_started_at_idx",
			"captain_model_calls_model_idx",
			"captain_model_calls_iteration_id_idx",
		} {
			Expect(indexDefinition(dropped)).To(BeEmpty(), dropped+" is no longer declared and must be dropped")
		}

		// The FK this backs is ON DELETE SET NULL, so it must survive -- just
		// without the NULL rows that made up every entry in it.
		Expect(indexDefinition("captain_messages_model_call_id_idx")).
			To(ContainSubstring("WHERE (model_call_id IS NOT NULL)"))
		// Still the referencing side of two foreign keys.
		Expect(indexDefinition("captain_model_calls_prompt_run_id_idx")).NotTo(BeEmpty())
	})
})
