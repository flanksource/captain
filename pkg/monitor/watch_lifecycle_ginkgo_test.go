package monitor

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("transcript watch lifecycle", func() {
	var (
		m       *Monitor
		w       *transcriptWatcher
		project string
	)

	BeforeEach(func() {
		project = GinkgoT().TempDir()
		m = &Monitor{cfg: Config{Debounce: time.Hour}, tracked: map[string]string{}}
		var err error
		w, err = newTranscriptWatcher(m, newIngestor(m))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(w.close)
	})

	It("releases the final session's directory and cancels its pending ingest", func(ctx SpecContext) {
		path := writeTranscript(project, "ended.jsonl")
		m.TrackTranscript(path, "claude")
		w.track(path, "claude")
		w.schedule(ctx, "claude", path)
		Expect(w.watcher.WatchList()).To(ContainElement(project))

		m.untrackTranscript(path)

		Expect(w.watcher.WatchList()).To(BeEmpty())
		Expect(w.tracked).To(BeEmpty())
		Expect(w.timers).To(BeEmpty())
	})

	It("retains a shared directory until its last active session ends", func() {
		first := writeTranscript(project, "first.jsonl")
		second := writeTranscript(project, "second.jsonl")
		for _, path := range []string{first, second} {
			m.TrackTranscript(path, "claude")
			w.track(path, "claude")
		}
		m.untrackTranscript(first)
		Expect(w.watcher.WatchList()).To(ConsistOf(project))
		Expect(w.tracked).To(Equal(map[string]string{second: "claude"}))
		m.untrackTranscript(second)
		Expect(w.watcher.WatchList()).To(BeEmpty())
	})

	It("arms and retires subagent watches with the live root transcript", func() {
		path := writeTranscript(project, "active.jsonl")
		writeTranscript(subagentsDir(path), "agent.jsonl")
		m.TrackTranscript(path, "claude")
		w.track(path, "claude")

		Expect(w.watcher.WatchList()).To(ConsistOf(project, filepath.Dir(subagentsDir(path)), subagentsDir(path)))
		m.untrackTranscript(path)
		Expect(w.watcher.WatchList()).To(BeEmpty())
	})

	It("discovers subagent directories created after the root was tracked", func(ctx SpecContext) {
		path := writeTranscript(project, "active.jsonl")
		m.TrackTranscript(path, "claude")
		w.track(path, "claude")
		Expect(os.Mkdir(filepath.Dir(subagentsDir(path)), 0o755)).To(Succeed())
		w.handle(ctx, fsnotify.Event{Name: filepath.Dir(subagentsDir(path)), Op: fsnotify.Create})
		Expect(w.watcher.WatchList()).To(ContainElement(filepath.Dir(subagentsDir(path))))
		writeTranscript(subagentsDir(path), "agent.jsonl")
		w.handle(ctx, fsnotify.Event{Name: subagentsDir(path), Op: fsnotify.Create})

		Expect(w.watcher.WatchList()).To(ConsistOf(project, filepath.Dir(subagentsDir(path)), subagentsDir(path)))
	})

	It("retires the previous Codex day directory when no live transcript needs it", func() {
		previous := filepath.Join(project, "previous-day")
		current := filepath.Join(project, "current-day")
		writeTranscript(previous, "old.jsonl")
		writeTranscript(current, "active.jsonl")
		w.watchDir(previous, "codex")
		Expect(w.syncProcesses(map[string]string{}, map[string]string{current: "codex"})).To(Succeed())
		Expect(w.watcher.WatchList()).To(ConsistOf(current))
	})

	It("does not schedule an ingest after the watcher has closed", func(ctx SpecContext) {
		path := writeTranscript(project, "closed.jsonl")
		w.close()
		w.schedule(ctx, "claude", path)
		Expect(w.timers).To(BeEmpty())
	})

	It("keeps a registration alive when its request context is cancelled", func() {
		path := writeTranscript(project, "registered.jsonl")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		armActiveWatchers(ctx, "claude", path)
		Expect(m.trackedPaths()).To(Equal(map[string]string{path: "claude"}))
	})

	It("retries a tracked directory that did not exist at registration time", func() {
		path := filepath.Join(project, "new-project", "active.jsonl")
		m.TrackTranscript(path, "claude")
		w.track(path, "claude")
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		w.track(path, "claude")
		Expect(w.watcher.WatchList()).To(ConsistOf(filepath.Dir(path)))
	})

	It("tails filesystem appends to an active subagent transcript", func() {
		path := writeTranscript(project, "active.jsonl")
		agent := writeTranscript(subagentsDir(path), "agent.jsonl")
		w.debounce = time.Millisecond
		ingested := make(chan string, 4)
		w.ingest = func(_ context.Context, _, path string) { ingested <- path }
		m.TrackTranscript(path, "claude")
		w.track(path, "claude")
		ctx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)
		go func() {
			defer GinkgoRecover()
			for {
				select {
				case <-ctx.Done():
					return
				case event := <-w.events():
					w.handle(ctx, event)
				case err := <-w.errors():
					Expect(err).NotTo(HaveOccurred())
				}
			}
		}()
		file, err := os.OpenFile(agent, os.O_APPEND|os.O_WRONLY, 0o600)
		Expect(err).NotTo(HaveOccurred())
		_, err = file.WriteString("\n")
		Expect(file.Close()).To(Succeed())
		Expect(err).NotTo(HaveOccurred())
		Eventually(ingested).Should(Receive(Equal(agent)))
	})
})
