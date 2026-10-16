package database

import (
	"sync"

	"github.com/flanksource/commons-db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gorm.io/gorm"
)

// Callers re-get an existing session far more often than they create one, so
// the found-and-unchanged path is the one whose cost matters.
var _ = Describe("CreateOrGetSession on an existing, unchanged session", func() {
	It("issues exactly one SELECT and returns the same session GetSession reads", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_get_existing_session"})
		db, err := Open(ctx, WithDSN(handle.DSN()), WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })

		input := CreateSessionInput{
			ProviderSessionID: "0199e2aa-0000-7000-8000-00000000f001", Source: "claude", HostID: "get-existing",
			CWD: "/work/existing", Path: "/home/dev/.claude/projects/-work-existing/session.jsonl",
		}
		created, err := db.CreateOrGetSession(ctx, input)
		Expect(err).NotTo(HaveOccurred())

		var (
			mu  sync.Mutex
			sql []string
		)
		capture := func(tx *gorm.DB) {
			mu.Lock()
			defer mu.Unlock()
			sql = append(sql, tx.Statement.SQL.String())
		}
		callbacks := db.Gorm().Callback()
		Expect(callbacks.Query().After("gorm:query").Register("test:count_query", capture)).To(Succeed())
		Expect(callbacks.Create().After("gorm:create").Register("test:count_create", capture)).To(Succeed())
		Expect(callbacks.Update().After("gorm:update").Register("test:count_update", capture)).To(Succeed())
		Expect(callbacks.Raw().After("gorm:raw").Register("test:count_raw", capture)).To(Succeed())

		existing, err := db.CreateOrGetSession(ctx, input)
		Expect(err).NotTo(HaveOccurred())
		mu.Lock()
		issued := append([]string(nil), sql...)
		mu.Unlock()
		Expect(issued).To(ConsistOf(HavePrefix("SELECT")))

		reread, err := db.GetSession(ctx, created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(existing).To(Equal(reread))
	})
})
