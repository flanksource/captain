package migrations

import (
	"github.com/flanksource/commons-db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Two write-path costs measured on a live database: gavel's per-session-write
// todo lookup joining captain_prompt_runs on root_session_id seq-scanned the
// table every time, and the process heartbeat table accumulated dead tuples ten
// times faster than its live set because no update could stay HOT.
var _ = Describe("Captain write-path schema", func() {
	It("indexes prompt runs by root session and gives the process table HOT room", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_write_path_schema"})
		Expect(Apply(ctx, handle.DSN())).To(Succeed())
		db := handle.SQL()

		var indexDefinition string
		Expect(db.QueryRowContext(ctx, `SELECT indexdef FROM pg_indexes
			WHERE schemaname = 'public' AND indexname = 'captain_prompt_runs_root_session_id_idx'`).
			Scan(&indexDefinition)).To(Succeed())
		Expect(indexDefinition).To(ContainSubstring("ON public.captain_prompt_runs USING btree (root_session_id)"))

		var options []string
		rows, err := db.QueryContext(ctx, `SELECT unnest(reloptions) FROM pg_class
			WHERE oid = 'public.captain_session_processes'::regclass`)
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(rows.Close()).To(Succeed()) }()
		for rows.Next() {
			var option string
			Expect(rows.Scan(&option)).To(Succeed())
			options = append(options, option)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		Expect(options).To(ConsistOf(
			"fillfactor=70",
			"autovacuum_vacuum_scale_factor=0.02",
			"autovacuum_analyze_scale_factor=0.02",
			"autovacuum_vacuum_cost_delay=0",
		))
	})
})
