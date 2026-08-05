package cli

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Read-only DSN rewriting", func() {
	DescribeTable("adds the read-only session option",
		func(dsn, expected string) {
			Expect(readOnlyDSN(dsn)).To(Equal(expected))
		},
		Entry("URL without a query",
			"postgres://reader@prod:5432/gavel",
			"postgres://reader@prod:5432/gavel?options=-c+default_transaction_read_only%3Don"),
		Entry("URL with an existing query",
			"postgres://reader@prod:5432/gavel?sslmode=disable",
			"postgres://reader@prod:5432/gavel?options=-c+default_transaction_read_only%3Don&sslmode=disable"),
		Entry("URL with existing options preserves them",
			"postgres://reader@prod:5432/gavel?options=-c+statement_timeout%3D5000",
			"postgres://reader@prod:5432/gavel?options=-c+statement_timeout%3D5000+-c+default_transaction_read_only%3Don"),
		Entry("keyword DSN",
			"host=prod dbname=gavel user=reader",
			"host=prod dbname=gavel user=reader options='-c default_transaction_read_only=on'"),
	)

	It("is idempotent for a DSN that is already read-only", func() {
		once, err := readOnlyDSN("postgres://reader@prod:5432/gavel")
		Expect(err).NotTo(HaveOccurred())

		Expect(readOnlyDSN(once)).To(Equal(once))
	})

	It("rejects a keyword DSN that already sets options", func() {
		_, err := readOnlyDSN("host=prod dbname=gavel options='-c statement_timeout=5000'")

		Expect(err).To(MatchError(ContainSubstring(`set "readOnly": false`)))
	})

	It("rejects an empty DSN", func() {
		_, err := readOnlyDSN("   ")

		Expect(err).To(MatchError(ContainSubstring("empty DSN")))
	})
})
