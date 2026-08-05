package cli

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/monitor"
	"github.com/flanksource/commons-db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Database context registry", Serial, func() {
	const secondaryContext = "secondary"

	var (
		defaultDB   *database.DB
		secondaryDB *database.DB
		discovered  int
	)

	// openLeasedDB leases an isolated migrated database from the shared test
	// server, so two contexts can be exercised without a second postgres.
	openLeasedDB := func(name string) *database.DB {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: name})
		db, err := database.Open(GinkgoT().Context(), database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
		return db
	}

	// seedLiveSession records one session and its running process, so the two
	// databases are distinguishable by their contents alone. session live lists
	// only sessions with an active process.
	seedLiveSession := func(ctx context.Context, db *database.DB, project, providerSessionID string) {
		session, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			Source: "codex", ProviderSessionID: providerSessionID, CWD: project,
		})
		Expect(err).NotTo(HaveOccurred())
		started := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
		Expect(db.UpsertSessionProcess(ctx, database.SessionProcessInput{
			SessionID: session.ID, HostID: captainHostID(), BootID: "boot", PID: 24680,
			ProcessStartedAt: started, SampledAt: started.Add(30 * time.Second),
			Status: "sleeping", CWD: project, Source: "codex",
		})).To(Succeed())
	}

	BeforeEach(func(ctx SpecContext) {
		databaseURLs = nil
		databaseContextFlagValue = ""
		home := GinkgoT().TempDir()
		GinkgoT().Setenv("HOME", home)
		GinkgoT().Setenv(databaseContextEnv, "")
		GinkgoT().Setenv(databaseContextsEnv, secondaryContext+"=postgres://unused/never-opened")
		resetCaptainContextsForTest()

		Expect(os.MkdirAll(filepath.Join(home, "work", "project"), 0o755)).To(Succeed())
		GinkgoT().Chdir(filepath.Join(home, "work", "project"))
		// Seed against the resolved working directory: on macOS the temp dir is
		// reached through a symlink, and session scoping matches cwd exactly.
		project, err := os.Getwd()
		Expect(err).NotTo(HaveOccurred())

		defaultDB = openLeasedDB("captain_ctx_default")
		secondaryDB = openLeasedDB("captain_ctx_secondary")
		seedLiveSession(ctx, defaultDB, project, "default-session")
		seedLiveSession(ctx, secondaryDB, project, "secondary-session")

		// Inject both handles so the registry answers from them rather than
		// dialing the placeholder DSN above.
		setCaptainContextDBForTest(testDatabaseHandle{Name: defaultDatabaseContextName, DB: defaultDB})
		setCaptainContextDBForTest(testDatabaseHandle{Name: secondaryContext, DB: secondaryDB, Source: "test secondary"})

		discovered = 0
		monitorDiscoverProcesses = func() ([]monitor.Process, error) {
			discovered++
			return nil, nil
		}
		DeferCleanup(func() {
			monitorDiscoverProcesses = nil
			resetCaptainContextsForTest()
		})
	})

	It("reads the default context when nothing selects one", func(ctx SpecContext) {
		result, err := RunSessionLive(ctx, SessionLiveOptions{Source: "all", All: true, Limit: 10})

		Expect(err).NotTo(HaveOccurred())
		Expect(sessionProviderIDs(result)).To(ConsistOf("default-session"))
	})

	It("reads the context bound to the request context", func(ctx SpecContext) {
		result, err := RunSessionLive(ContextWithDatabaseContext(ctx, secondaryContext), SessionLiveOptions{Source: "all", All: true, Limit: 10})

		Expect(err).NotTo(HaveOccurred())
		Expect(sessionProviderIDs(result)).To(ConsistOf("secondary-session"))
	})

	It("reports the active context's database identity, not the default's", func(ctx SpecContext) {
		result, err := RunSessionLive(ContextWithDatabaseContext(ctx, secondaryContext), SessionLiveOptions{Source: "all", All: true, Limit: 10})

		Expect(err).NotTo(HaveOccurred())
		Expect(result.Database.Source).To(Equal("test secondary"))
	})

	It("keeps writes on the default context while a secondary is active", func(ctx SpecContext) {
		db, err := captainDefaultDB(ContextWithDatabaseContext(ctx, secondaryContext))

		Expect(err).NotTo(HaveOccurred())
		Expect(db).To(BeIdenticalTo(defaultDB))
	})

	It("runs the monitor pass when freshening the default context", func(ctx SpecContext) {
		_, err := freshenSessionDB(ctx)

		Expect(err).NotTo(HaveOccurred())
		Expect(discovered).To(BeNumerically(">", 0))
	})

	It("never writes to a secondary context when freshening it", func(ctx SpecContext) {
		db, err := freshenSessionDB(ContextWithDatabaseContext(ctx, secondaryContext))

		Expect(err).NotTo(HaveOccurred())
		Expect(db).To(BeIdenticalTo(secondaryDB))
		Expect(discovered).To(Equal(0), "a read of another machine's database must not run a monitor pass")
	})

	It("refuses to migrate a secondary context", func(ctx SpecContext) {
		setCaptainContextDBForTest(testDatabaseHandle{Name: secondaryContext, DB: nil})

		_, err := openContextDB(ctx, secondaryContext, captainDatabaseWithMigrations)

		Expect(err).To(MatchError(ContainSubstring(`captain only migrates the "default" database context`)))
	})

	It("retries an open that failed rather than memoizing the error", func(ctx SpecContext) {
		setCaptainContextDBForTest(testDatabaseHandle{Name: secondaryContext, DB: nil})

		_, first := openContextDB(ctx, secondaryContext, captainDatabaseNoMigrations)
		Expect(first).To(HaveOccurred())

		setCaptainContextDBForTest(testDatabaseHandle{Name: secondaryContext, DB: secondaryDB})
		db, err := openContextDB(ctx, secondaryContext, captainDatabaseNoMigrations)

		Expect(err).NotTo(HaveOccurred())
		Expect(db).To(BeIdenticalTo(secondaryDB))
	})
})

// sessionProviderIDs identifies which database answered a read.
func sessionProviderIDs(result SessionLiveResult) []string {
	ids := make([]string, 0, len(result.Sessions))
	for _, session := range result.Sessions {
		ids = append(ids, session.ID)
	}
	return ids
}
