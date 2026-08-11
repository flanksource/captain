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
			migrate: func(_ context.Context, dsn, schemaName string) error {
				calls = append(calls, "migrate:"+dsn+":"+schemaName)
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
		Expect(calls).To(Equal([]string{"migrate:postgres://captain:public", "open:postgres://captain"}))
	})

	It("scopes migrations and Captain-owned pools to the selected schema", func(ctx SpecContext) {
		db, err := open(ctx, deps,
			WithDSN("postgres://captain/database?sslmode=disable"),
			WithSchema("agent_namespace_context"),
			WithMigrations(),
		)

		Expect(err).NotTo(HaveOccurred())
		Expect(db.Schema()).To(Equal("agent_namespace_context"))
		Expect(calls).To(Equal([]string{
			"migrate:postgres://captain/database?sslmode=disable:agent_namespace_context",
			"open:postgres://captain/database?search_path=agent_namespace_context&sslmode=disable",
		}))
	})

	It("rejects an invalid explicit schema before migrations or pool creation", func(ctx SpecContext) {
		_, err := open(ctx, deps, WithDSN("postgres://captain"), WithSchema(""), WithMigrations())

		Expect(err).To(MatchError(ContainSubstring("captain database schema")))
		Expect(calls).To(BeEmpty())
	})

	It("rejects a non-public schema on a host-owned pool", func(ctx SpecContext) {
		_, err := open(ctx, deps, WithGorm(&gorm.DB{}), WithSchema("agent_namespace_context"))

		Expect(err).To(MatchError(ContainSubstring("host-owned GORM pool")))
		Expect(calls).To(BeEmpty())
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
		deps.migrate = func(context.Context, string, string) error { return migrationErr }

		_, err := open(ctx, deps, WithDSN("postgres://captain"), WithMigrations())

		Expect(err).To(MatchError(migrationErr))
		Expect(calls).To(BeEmpty())
	})
})
