package cli

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gorm.io/gorm"

	"github.com/flanksource/captain/pkg/database"
)

var _ = Describe("Captain database migration mode", Serial, func() {
	AfterEach(func() {
		setCaptainDBForTest(nil)
	})

	It("rejects serve startup after a non-migrating handle was installed", func(ctx SpecContext) {
		db, err := database.Use(&gorm.DB{})
		Expect(err).NotTo(HaveOccurred())
		setCaptainContextDBForTest(testDatabaseHandle{
			Name: defaultDatabaseContextName, DB: db, Unmigrated: true,
		})

		_, err = captainServeDB(ctx)

		Expect(err).To(MatchError("captain serve cannot migrate after the process database was opened without migrations"))
	})

	It("reuses a migration-initialized handle", func(ctx SpecContext) {
		db, err := database.Use(&gorm.DB{})
		Expect(err).NotTo(HaveOccurred())
		setCaptainDBForTest(db)

		opened, err := captainServeDB(ctx)

		Expect(err).NotTo(HaveOccurred())
		Expect(opened).To(BeIdenticalTo(db))
	})
})
