package monitor

import (
	"os"
	"path/filepath"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Claude Code links a resumed session's sub-agent transcripts to the original
// session's files with absolute symlinks. The target is ingested under its own
// path; the link names the same agent under a second parent.
var _ = Describe("symlinked transcripts", Ordered, func() {
	var db *database.DB

	BeforeAll(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_monitor_symlinked_transcript"})
		var err error
		db, err = database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
	})

	It("skips a sub-agent transcript that links to another session's file", func(ctx SpecContext) {
		GinkgoT().Setenv("HOME", GinkgoT().TempDir())
		project := filepath.Join(claude.GetProjectsDir(), "-work-example")
		original := filepath.Join(project, "0199e2aa-0000-7000-8000-0000000a0001", "subagents", "agent-a1.jsonl")
		link := filepath.Join(project, "0199e2aa-0000-7000-8000-0000000a0002", "subagents", "agent-a1.jsonl")
		Expect(os.MkdirAll(filepath.Dir(original), 0o755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Dir(link), 0o755)).To(Succeed())
		Expect(os.WriteFile(original, []byte("{}\n"), 0o600)).To(Succeed())
		Expect(os.Symlink(original, link)).To(Succeed())

		monitor, err := New(Config{DB: db, HostID: "symlink-test"})
		Expect(err).NotTo(HaveOccurred())
		Expect(newIngestor(monitor).ingestFile(ctx, "claude", link)).To(Succeed())

		var sources int64
		Expect(db.Gorm().Raw(`SELECT count(*) FROM captain_session_sources WHERE path = ?`, link).
			Scan(&sources).Error).To(Succeed())
		Expect(sources).To(BeZero())
	})
})
