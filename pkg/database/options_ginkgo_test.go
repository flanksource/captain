package database

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gorm.io/gorm"
)

var _ = Describe("Open options", func() {
	var (
		calls  []string
		opened *gorm.DB
		deps   dependencies
	)

	BeforeEach(func() {
		calls = nil
		opened = &gorm.DB{}
		deps = dependencies{
			migrate: func(_ context.Context, dsn string) error {
				calls = append(calls, "migrate:"+dsn)
				return nil
			},
			open: func(dsn string, _ *gorm.Config) (*gorm.DB, error) {
				calls = append(calls, "open:"+dsn)
				return opened, nil
			},
		}
	})

	It("opens without migrating by default", func(ctx SpecContext) {
		db, err := open(ctx, deps, WithDSN(" postgres://captain "))

		Expect(err).NotTo(HaveOccurred())
		Expect(db.Gorm()).To(BeIdenticalTo(opened))
		Expect(calls).To(Equal([]string{"open:postgres://captain"}))
	})

	It("migrates before opening when explicitly requested", func(ctx SpecContext) {
		db, err := open(ctx, deps, WithDSN("postgres://captain"), WithMigrations())

		Expect(err).NotTo(HaveOccurred())
		Expect(db.Gorm()).To(BeIdenticalTo(opened))
		Expect(calls).To(Equal([]string{"migrate:postgres://captain", "open:postgres://captain"}))
	})

	It("reuses an injected pool without a DSN by default", func(ctx SpecContext) {
		shared := &gorm.DB{}
		db, err := open(ctx, deps, WithGorm(shared))

		Expect(err).NotTo(HaveOccurred())
		Expect(db.Gorm()).To(BeIdenticalTo(shared))
		Expect(calls).To(BeEmpty())
	})

	It("requires a DSN when migrations are requested", func(ctx SpecContext) {
		_, err := open(ctx, deps, WithGorm(&gorm.DB{}), WithMigrations())

		Expect(err).To(MatchError("captain database migrations require a DSN"))
		Expect(calls).To(BeEmpty())
	})

	It("does not open after an explicit migration fails", func(ctx SpecContext) {
		migrationErr := errors.New("migration failed")
		deps.migrate = func(context.Context, string) error { return migrationErr }

		_, err := open(ctx, deps, WithDSN("postgres://captain"), WithMigrations())

		Expect(err).To(MatchError(migrationErr))
		Expect(calls).To(BeEmpty())
	})
})
