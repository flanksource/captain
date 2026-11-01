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

	It("does not register a built-in review prompt source", func() {
		sources, err := buildPromptSources(context.Background(), promptSourceOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(sources).NotTo(ContainElement(HaveField("ID", "builtin")))
	})

	It("requires a local source for review prompts", func() {
		_, err := resolvePromptRecord(context.Background(), promptRecordOptions{ID: "spec-internal-high"})
		Expect(err).To(HaveOccurred())
	})

	It("discovers prompts in the user configuration directory", func() {
		path := writeUserPrompt("triage.prompt", "Triage {{issue}}.\n")
		record, err := resolvePromptRecord(context.Background(), promptRecordOptions{ID: "triage"})
		Expect(err).NotTo(HaveOccurred())
		Expect(record.Path).To(Equal(path))
		Expect(record.Source.Implicit).To(BeTrue())
	})

	It("resolves a review prompt installed in the user directory", func() {
		path := writeUserPrompt("spec-internal-high.prompt", "Review {{files}} my way.\n")
		record, err := resolvePromptRecord(context.Background(), promptRecordOptions{ID: "spec-internal-high"})
		Expect(err).NotTo(HaveOccurred())
		Expect(record.Path).To(Equal(path))
	})

	It("prefers the project prompt to the linked user library", func() {
		project := GinkgoT().TempDir()
		library := GinkgoT().TempDir()
		Expect(os.MkdirAll(promptsDir, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(project, "branch-rebase.prompt"), []byte("Project rebase."), 0o600)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(library, "branch-rebase.prompt"), []byte("Library rebase."), 0o600)).To(Succeed())
		Expect(os.Symlink(library, filepath.Join(promptsDir, "library"))).To(Succeed())
		ctx := ContextWithPromptSources(context.Background(), PromptSources{Dirs: []string{project}})
		for _, name := range []string{"branch-rebase", "branch-rebase.prompt"} {
			detail, err := getPrompt(ctx, name)
			Expect(err).NotTo(HaveOccurred())
			Expect(detail.Content).To(Equal("Project rebase."))
		}
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
