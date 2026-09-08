package monitor

import (
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Filesystem discovery is a snapshot of where agents have already been seen to
// run. A registration is the other half: it names a transcript by session id, so
// a recon can pick up a worktree's project directory that no glob had reason to
// visit when it was created.
var _ = Describe("backfill over registered transcripts", Ordered, func() {
	var monitor *Monitor

	BeforeAll(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_monitor_registered"})
		db, err := database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
		monitor, err = New(Config{DB: db, HostID: "registered-test"})
		Expect(err).NotTo(HaveOccurred())
	})

	register := func(ctx SpecContext, providerSessionID, path string) {
		GinkgoHelper()
		session, err := monitor.db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: providerSessionID, Source: "claude", HostID: "registered-test",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(monitor.db.RegisterTranscriptSource(ctx, session.ID, "claude", path, providerSessionID)).To(Succeed())
	}

	It("scans a registered transcript the filesystem never offered", func(ctx SpecContext) {
		// An empty home is the worktree case in miniature: the project directory
		// the run writes into is not one any scan of this machine turns up.
		GinkgoT().Setenv("HOME", GinkgoT().TempDir())
		path := "/Users/dev/.claude/projects/-worktree-shell-9f3a/0199e2aa-0000-7000-8000-000000000001.jsonl"
		register(ctx, "0199e2aa-0000-7000-8000-000000000001", path)

		roots, _ := monitor.transcriptScanSet(ctx)
		Expect(roots).To(ContainElement(transcriptRef{source: "claude", path: path}))
	})

	It("does not queue a transcript the scan already found", func(ctx SpecContext) {
		path := "/Users/dev/.claude/projects/-repo/0199e2aa-0000-7000-8000-000000000002.jsonl"
		register(ctx, "0199e2aa-0000-7000-8000-000000000002", path)
		discovered := []transcriptRef{{source: "claude", path: path}}

		Expect(monitor.registeredTranscripts(ctx, discovered, nil)).
			NotTo(ContainElement(transcriptRef{source: "claude", path: path}))
	})
})
