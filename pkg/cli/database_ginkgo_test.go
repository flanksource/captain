package cli

import (
	"os"
	"path/filepath"

	"github.com/flanksource/captain/pkg/database"
	commonsdb "github.com/flanksource/commons-db/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func withGinkgoCaptainDB() {
	if os.Getenv("CAPTAIN_DB_EMBEDDED_TEST") == "" {
		Skip("set CAPTAIN_DB_EMBEDDED_TEST=1 to run embedded-postgres cli tests")
	}
	dsn, stop, err := commonsdb.StartEmbedded(commonsdb.EmbeddedConfig{
		DataDir:  filepath.Join(GinkgoT().TempDir(), "postgres"),
		Database: "captain_cli",
	})
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { Expect(stop()).To(Succeed()) })

	db, err := database.Open(GinkgoT().Context(), database.WithDSN(dsn), database.WithMigrations())
	Expect(err).NotTo(HaveOccurred())
	setCaptainDBForTest(db)
	DeferCleanup(func() {
		setCaptainDBForTest(nil)
		Expect(db.Close()).To(Succeed())
	})
}
