package monitor

import (
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("monitor watch ownership", func() {
	var (
		m         *Monitor
		w         *transcriptWatcher
		ing       *ingestor
		path      string
		processes []Process
	)
	BeforeEach(func(ctx SpecContext) {
		home := GinkgoT().TempDir()
		GinkgoT().Setenv("HOME", home)
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_monitor_watch_lifecycle"})
		db, err := database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
		project := filepath.Join(home, ".claude", "projects", "-home-dev-example")
		Expect(os.MkdirAll(project, 0o755)).To(Succeed())
		path = filepath.Join(project, fixtureSessionID+".jsonl")
		Expect(os.WriteFile(path, []byte(fixtureTranscript), 0o600)).To(Succeed())
		writeTranscript(subagentsDir(path), "agent.jsonl")
		processes = nil
		m, err = New(Config{DB: db, HostID: "watch-lifecycle", DiscoverProcesses: func() ([]Process, error) { return processes, nil }})
		Expect(err).NotTo(HaveOccurred())
		ing = newIngestor(m)
		w, err = newTranscriptWatcher(m, ing)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(w.close)
	})

	It("ingests historical root transcripts without adding watches", func(ctx SpecContext) {
		Expect(ing.ingestFile(ctx, "claude", path)).To(Succeed())
		Expect(w.watcher.WatchList()).To(BeEmpty())
	})

	It("releases session watches on SessionEnd", func(ctx SpecContext) {
		m.handleHookEvent(ctx, w, ing, HookEvent{Provider: "claude", Event: "SessionStart", SessionID: fixtureSessionID, TranscriptPath: path})
		Expect(w.watcher.WatchList()).To(ContainElement(filepath.Dir(path)))
		m.handleHookEvent(ctx, w, ing, HookEvent{Provider: "claude", Event: "SessionEnd", SessionID: fixtureSessionID, TranscriptPath: path})
		Expect(w.watcher.WatchList()).To(BeEmpty())
	})

	It("reaps watches and explicit tracking when an observed process vanishes without SessionEnd", func(ctx SpecContext) {
		Expect(ing.ingestFile(ctx, "claude", path)).To(Succeed())
		started := time.Now().Add(-time.Minute)
		processes = []Process{{Source: "claude", PID: 4242, CWD: fixtureCWD, StartedAt: &started, Command: "claude --resume " + fixtureSessionID}}
		m.TrackTranscript(path, "claude")
		Expect(m.pollProcesses(ctx, w)).To(Succeed())
		Expect(w.watcher.WatchList()).To(ContainElement(subagentsDir(path)))
		processes = nil
		Expect(m.pollProcesses(ctx, w)).To(Succeed())
		Expect(w.watcher.WatchList()).To(BeEmpty())
		Expect(m.trackedPaths()).To(BeEmpty())
	})

	It("preserves live watches and reports a failed process sample", func(ctx SpecContext) {
		Expect(ing.ingestFile(ctx, "claude", path)).To(Succeed())
		started := time.Now().Add(-time.Minute)
		processes = []Process{{Source: "claude", PID: 4242, CWD: fixtureCWD, StartedAt: &started, Command: "claude --resume " + fixtureSessionID}}
		Expect(m.pollProcesses(ctx, w)).To(Succeed())
		before := w.watcher.WatchList()
		Expect(m.db.Close()).To(Succeed())
		processes[0].Status = "active"

		Expect(m.pollProcesses(ctx, w)).NotTo(Succeed())
		Expect(w.watcher.WatchList()).To(ConsistOf(before))
	})

	It("reports a missing session instead of treating its transcript path as empty", func(ctx SpecContext) {
		_, err := m.sessionTranscriptPath(ctx, uuid.New())
		Expect(err).To(HaveOccurred())
	})
})
