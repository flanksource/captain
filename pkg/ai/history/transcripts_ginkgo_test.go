package history

import (
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("FindTranscripts", func() {
	var (
		home                                string
		oldRoot, newRoot, newAgent, resumed string
		oldCodex                            string
		lastWeek, yesterday, now            time.Time
	)

	write := func(path string, modTime time.Time) string {
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		Expect(os.WriteFile(path, []byte("{}\n"), 0o600)).To(Succeed())
		Expect(os.Chtimes(path, modTime, modTime)).To(Succeed())
		return path
	}

	BeforeEach(func() {
		home = GinkgoT().TempDir()
		now = time.Now().Truncate(time.Second)
		yesterday = now.Add(-24 * time.Hour)
		lastWeek = now.Add(-7 * 24 * time.Hour)
		projects := filepath.Join(home, ".claude", "projects", "-work-repo")
		sessions := filepath.Join(home, ".codex", "sessions")
		oldRoot = write(filepath.Join(projects, "old-session.jsonl"), lastWeek)
		newRoot = write(filepath.Join(projects, "new-session.jsonl"), now)
		newAgent = write(filepath.Join(projects, "new-session", "subagents", "agent-a1.jsonl"), now)
		oldCodex = write(filepath.Join(sessions, "2026", "10", "01", "rollout-2026-10-01T00-00-00-old.jsonl"), lastWeek)
		// A resumed codex session stays under the folder of the day it started.
		resumed = write(filepath.Join(sessions, "2026", "10", "01", "rollout-2026-10-01T01-00-00-resumed.jsonl"), now)
		write(filepath.Join(sessions, "2026", "10", "01", "notes.txt"), now)
	})

	paths := func(found []Transcript) []Transcript {
		for i := range found {
			found[i].Info = nil
		}
		return found
	}

	It("returns every transcript under the home when no time is given", func() {
		found, err := FindTranscripts(TranscriptQuery{Home: home})

		Expect(err).NotTo(HaveOccurred())
		Expect(paths(found)).To(ConsistOf(
			Transcript{Source: TranscriptSourceClaude, Kind: TranscriptRoot, Path: oldRoot},
			Transcript{Source: TranscriptSourceClaude, Kind: TranscriptRoot, Path: newRoot},
			Transcript{Source: TranscriptSourceClaude, Kind: TranscriptAgent, Path: newAgent},
			Transcript{Source: TranscriptSourceCodex, Kind: TranscriptRoot, Path: oldCodex},
			Transcript{Source: TranscriptSourceCodex, Kind: TranscriptRoot, Path: resumed},
		))
	})

	It("returns only the transcripts modified at or after ModifiedSince, wherever their day folder is", func() {
		found, err := FindTranscripts(TranscriptQuery{Home: home, ModifiedSince: yesterday})

		Expect(err).NotTo(HaveOccurred())
		Expect(paths(found)).To(ConsistOf(
			Transcript{Source: TranscriptSourceClaude, Kind: TranscriptRoot, Path: newRoot},
			Transcript{Source: TranscriptSourceClaude, Kind: TranscriptAgent, Path: newAgent},
			Transcript{Source: TranscriptSourceCodex, Kind: TranscriptRoot, Path: resumed},
		))
	})

	It("keeps a transcript modified exactly at ModifiedSince", func() {
		found, err := FindTranscripts(TranscriptQuery{Home: home, ModifiedSince: lastWeek})

		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(HaveLen(5))
	})

	It("returns nothing when every transcript is older than ModifiedSince", func() {
		found, err := FindTranscripts(TranscriptQuery{Home: home, ModifiedSince: now.Add(time.Hour)})

		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeEmpty())
	})

	It("carries the file info the filter used", func() {
		found, err := FindTranscripts(TranscriptQuery{Home: home, ModifiedSince: yesterday})

		Expect(err).NotTo(HaveOccurred())
		for _, transcript := range found {
			Expect(transcript.Info).NotTo(BeNil(), transcript.Path)
			Expect(transcript.Info.ModTime()).To(Equal(now), transcript.Path)
		}
	})

	It("returns nothing for a home without agent transcripts", func() {
		found, err := FindTranscripts(TranscriptQuery{Home: GinkgoT().TempDir()})

		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeEmpty())
	})
})
