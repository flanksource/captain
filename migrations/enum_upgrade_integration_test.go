package migrations

import (
	"github.com/flanksource/commons-db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Captain migration upgrades", func() {
	It("appends partial to the existing session lifecycle enum", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_enum_upgrade"})
		dsn, db := handle.DSN(), handle.SQL()
		var err error
		_, err = db.ExecContext(ctx, `CREATE TYPE public.captain_session_lifecycle_status AS ENUM (
			'created', 'running', 'succeeded', 'failed', 'cancelled', 'interrupted'
		)`)
		Expect(err).NotTo(HaveOccurred())

		Expect(Apply(ctx, dsn)).To(Succeed())
		Expect(Apply(ctx, dsn)).To(Succeed())

		rows, err := db.QueryContext(ctx, `
		SELECT enumlabel
		FROM pg_catalog.pg_enum
		JOIN pg_catalog.pg_type ON pg_type.oid = pg_enum.enumtypid
		WHERE pg_type.typname = 'captain_session_lifecycle_status'
		ORDER BY enumsortorder
	`)
		Expect(err).NotTo(HaveOccurred())
		defer rows.Close()

		var values []string
		for rows.Next() {
			var value string
			Expect(rows.Scan(&value)).To(Succeed())
			values = append(values, value)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		Expect(values).To(Equal(
			[]string{"created", "running", "succeeded", "partial", "failed", "cancelled", "interrupted"},
		))
	})

	// Captain declares a stable scope so it can share a database with other
	// independently migrated applications (see the package doc). Honouring that
	// means the realm diff must not destroy objects Captain does not declare:
	// every Apply inspects the host's tables and enums too, and wants to drop
	// them all. Suppressing only table drops used to leave the enum drop in the
	// plan, where it failed against the very column it had just preserved —
	// permanently breaking store-open for any binary carrying an older bundle.
	It("leaves a co-tenant's undeclared enum and its table intact", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_co_tenant_enum"})
		dsn, db := handle.DSN(), handle.SQL()

		_, err := db.ExecContext(ctx,
			`CREATE TYPE public.host_task_status AS ENUM ('dispatched', 'running', 'errored')`)
		Expect(err).NotTo(HaveOccurred())
		_, err = db.ExecContext(ctx, `CREATE TABLE public.host_tasks (
			id text PRIMARY KEY,
			status public.host_task_status NOT NULL DEFAULT 'dispatched'
		)`)
		Expect(err).NotTo(HaveOccurred())

		Expect(Apply(ctx, dsn)).To(Succeed())
		Expect(Apply(ctx, dsn)).To(Succeed())

		var types int
		Expect(db.QueryRowContext(ctx,
			`SELECT count(*) FROM pg_catalog.pg_type WHERE typname = 'host_task_status'`).
			Scan(&types)).To(Succeed())
		Expect(types).To(Equal(1), "co-tenant enum should survive Captain's migration")

		// Writing through the column proves it still resolves its enum type, not
		// just that a row in pg_type survived.
		_, err = db.ExecContext(ctx,
			`INSERT INTO public.host_tasks (id, status) VALUES ('t1', 'running')`)
		Expect(err).NotTo(HaveOccurred())

		var status string
		Expect(db.QueryRowContext(ctx,
			`SELECT status FROM public.host_tasks WHERE id = 't1'`).Scan(&status)).To(Succeed())
		Expect(status).To(Equal("running"))
	})
})
