package cli

import (
	"context"
	"os"
	"path/filepath"

	"github.com/flanksource/captain/pkg/captainconfig"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Prompt source discovery", func() {
	var promptsDir string
	builtinNames := []string{"spec-internal-high.prompt", "spec-internal-low.prompt", "spec-public-high.prompt", "spec-public-low.prompt", "proposal-review.prompt"}

	BeforeEach(func() {
		captainconfig.SetPathForTesting(filepath.Join(GinkgoT().TempDir(), ".captain.yaml"))
		DeferCleanup(func() { captainconfig.SetPathForTesting("") })
		configHome := GinkgoT().TempDir()
		GinkgoT().Setenv("XDG_CONFIG_HOME", configHome)
		promptsDir = filepath.Join(configHome, "captain", "prompts")
	})

	writeUserPrompt := func(name, body string) string {
		Expect(os.MkdirAll(promptsDir, 0o755)).To(Succeed())
		path := filepath.Join(promptsDir, name)
		Expect(os.WriteFile(path, []byte(body), 0o600)).To(Succeed())
		return path
	}

	It("ships the review prompts as a read-only built-in source", func() {
		sources, err := buildPromptSources(context.Background(), promptSourceOptions{})
		Expect(err).NotTo(HaveOccurred())
		var builtin []promptRecord
		for _, source := range sources {
			if source.ID == "builtin" {
				Expect(source.Writable).To(BeFalse())
				builtin, err = listPromptRecordsFromSource(source)
				Expect(err).NotTo(HaveOccurred())
			}
		}
		names := make([]string, 0, len(builtin))
		for _, record := range builtin {
			names = append(names, filepath.Base(record.Rel))
		}
		Expect(names).To(ConsistOf(builtinNames))
	})

	It("resolves built-in prompts by name when the user prompt directory is absent", func() {
		record, err := resolvePromptRecord(context.Background(), promptRecordOptions{ID: "spec-internal-high"})
		Expect(err).NotTo(HaveOccurred())
		Expect(record.Source.ID).To(Equal("builtin"))
		Expect(record.Rel).To(Equal("builtin/spec-internal-high.prompt"))
	})

	It("discovers prompts in the user configuration directory", func() {
		path := writeUserPrompt("triage.prompt", "Triage {{issue}}.\n")
		record, err := resolvePromptRecord(context.Background(), promptRecordOptions{ID: "triage"})
		Expect(err).NotTo(HaveOccurred())
		Expect(record.Path).To(Equal(path))
		Expect(record.Source.Implicit).To(BeTrue())
	})

	It("lets a user copy shadow a built-in prompt of the same name", func() {
		path := writeUserPrompt("spec-internal-high.prompt", "Review {{files}} my way.\n")
		record, err := resolvePromptRecord(context.Background(), promptRecordOptions{ID: "spec-internal-high"})
		Expect(err).NotTo(HaveOccurred())
		Expect(record.Path).To(Equal(path))
	})

	It("keeps the repository prompt directory as the default save target", func() {
		sources, err := buildPromptSources(context.Background(), promptSourceOptions{})
		Expect(err).NotTo(HaveOccurred())
		writable, ok := firstWritableSource(sources)
		Expect(ok).To(BeTrue())
		Expect(writable.Root).To(HaveSuffix(filepath.Join(".captain", "prompts")))
		Expect(sources).To(ContainElement(SatisfyAll(HaveField("Root", promptsDir), HaveField("Implicit", true), HaveField("Writable", true))))
	})
})
