package cli

import (
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func withGinkgoCaptainDB() {
	handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_cli"})
	db, err := database.Open(GinkgoT().Context(), database.WithDSN(handle.DSN()), database.WithMigrations())
	Expect(err).NotTo(HaveOccurred())
	setCaptainDBForTest(db)
	DeferCleanup(func() {
		setCaptainDBForTest(nil)
		Expect(db.Close()).To(Succeed())
	})
}
