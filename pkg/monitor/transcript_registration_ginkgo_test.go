package monitor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A run inside a fresh git worktree writes into a project directory named after
// the worktree, not the repository — nothing that scans by working directory has
// ever heard of it. The provider session id is the only handle that exists at
// run start, so resolution has to work from it alone.
const (
	worktreeProjectDir = "-Users-dev-go-src-example-shell-worktrees-shell-9f3a-0198"
	repoProjectDir     = "-Users-dev-go-src-example"
)

func writeTranscript(dir, name string) string {
	GinkgoHelper()
	Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
	path := filepath.Join(dir, name)
	Expect(os.WriteFile(path, []byte(`{"type":"summary"}`+"\n"), 0o600)).To(Succeed())
	return path
}

var _ = Describe("transcript registration", func() {
	var home string

	BeforeEach(func() {
		home = GinkgoT().TempDir()
		GinkgoT().Setenv("HOME", home)
	})

	Describe("findTranscriptPath", func() {
		It("resolves a claude transcript by session id from a project directory no cwd scan would reach", func() {
			sessionID := "0199aa1b-2c3d-7e4f-8a9b-0c1d2e3f4a5b"
			writeTranscript(filepath.Join(home, ".claude", "projects", repoProjectDir), "other-session.jsonl")
			want := writeTranscript(filepath.Join(home, ".claude", "projects", worktreeProjectDir), sessionID+".jsonl")

			Expect(findTranscriptPath("claude", sessionID)).To(Equal(want))
		})

		It("reports a session whose transcript has not been written yet as not found", func() {
			_, err := findTranscriptPath("claude", "0199aa1b-2c3d-7e4f-8a9b-000000000000")
			Expect(err).To(MatchError(ErrTranscriptNotFound))
		})

		It("resolves a codex rollout by the session id embedded in its filename", func() {
			sessionID := "0199bb2c-3d4e-7f50-9a1b-2c3d4e5f6a7b"
			day := filepath.Join(home, ".codex", "sessions", "2026", "09", "07")
			writeTranscript(day, "rollout-2026-09-07T09-00-00-0199bb2c-0000-0000-0000-000000000000.jsonl")
			want := writeTranscript(day, "rollout-2026-09-07T11-31-02-"+sessionID+".jsonl")

			Expect(findTranscriptPath("codex", sessionID)).To(Equal(want))
		})

		It("rejects a source that writes no transcript instead of guessing one", func() {
			_, err := findTranscriptPath("gavel", "0199aa1b-2c3d-7e4f-8a9b-0c1d2e3f4a5b")
			Expect(err).To(MatchError(ContainSubstring(`unknown transcript source "gavel"`)))
			Expect(errors.Is(err, ErrTranscriptNotFound)).To(BeFalse(),
				"an unsupported source is a programming error, not a transcript that may still appear")
		})

		It("requires a provider session id", func() {
			_, err := findTranscriptPath("claude", "  ")
			Expect(err).To(MatchError(ContainSubstring("provider session ID is required")))
		})
	})

	Describe("armActiveWatchers", func() {
		It("tails a registered transcript and ingests it once without waiting for a poll", func(ctx SpecContext) {
			sessionID := "0199cc3d-4e5f-7061-8b2c-3d4e5f6a7b8c"
			path := writeTranscript(filepath.Join(home, ".claude", "projects", worktreeProjectDir), sessionID+".jsonl")

			monitor := &Monitor{cfg: Config{Debounce: time.Millisecond}, tracked: map[string]string{}}
			watcher, err := newTranscriptWatcher(monitor, newIngestor(monitor))
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(watcher.close)

			ingested := make(chan string, 4)
			watcher.ingest = func(_ context.Context, source, ingestedPath string) {
				ingested <- source + " " + ingestedPath
			}

			armActiveWatchers(ctx, "claude", path)

			Eventually(ingested).Should(Receive(Equal("claude " + path)))
			source, ok := watcher.classify(path)
			Expect(ok).To(BeTrue(), "the registered transcript must stay tailed for later appends")
			Expect(source).To(Equal("claude"))
		})

		It("does not hand transcripts to a watcher that has been closed", func(ctx SpecContext) {
			path := writeTranscript(filepath.Join(home, ".claude", "projects", worktreeProjectDir), "closed.jsonl")
			monitor := &Monitor{cfg: Config{Debounce: time.Millisecond}, tracked: map[string]string{}}
			watcher, err := newTranscriptWatcher(monitor, newIngestor(monitor))
			Expect(err).NotTo(HaveOccurred())
			ingested := make(chan string, 4)
			watcher.ingest = func(_ context.Context, _, ingestedPath string) { ingested <- ingestedPath }
			watcher.close()

			armActiveWatchers(ctx, "claude", path)

			Consistently(ingested, 50*time.Millisecond).ShouldNot(Receive())
		})
	})
})
