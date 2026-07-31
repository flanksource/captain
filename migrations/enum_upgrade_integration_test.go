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
})
