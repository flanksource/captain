package history

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Codex session discovery", func() {
	It("finds rollouts written by a read-only run in captain's isolated CODEX_HOME", func() {
		root := GinkgoT().TempDir()
		GinkgoT().Setenv("CODEX_HOME", filepath.Join(root, "user-codex"))
		GinkgoT().Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
		user := filepath.Join(root, "user-codex", "sessions", "2026", "10", "22", "rollout-a.jsonl")
		readOnly := filepath.Join(root, "config", "captain", "codex-read-only", "sessions", "2026", "10", "22", "rollout-b.jsonl")
		for _, path := range []string{user, readOnly} {
			Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
			Expect(os.WriteFile(path, []byte("{}\n"), 0o600)).To(Succeed())
		}

		files, err := FindCodexSessionFiles()

		Expect(err).NotTo(HaveOccurred())
		Expect(files).To(ConsistOf(user, readOnly))
		Expect(IsCodexSession(readOnly)).To(BeTrue())
	})
})
