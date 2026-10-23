package cli

import (
	"context"
	"os"
	"path/filepath"

	"github.com/flanksource/captain/pkg/captainconfig"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("linked prompts", func() {
	var source promptSource
	var target string
	var ctx context.Context

	BeforeEach(func() {
		captainconfig.SetPathForTesting(filepath.Join(GinkgoT().TempDir(), ".captain.yaml"))
		DeferCleanup(captainconfig.SetPathForTesting, "")
		GinkgoT().Setenv("XDG_CONFIG_HOME", GinkgoT().TempDir())
		source = promptSource{Kind: "local", Root: GinkgoT().TempDir(), Writable: true}
		var err error
		source.Root, err = filepath.EvalSymlinks(source.Root)
		Expect(err).NotTo(HaveOccurred())
		source.ID = hashPromptDir(source.Root)
		source.Label = source.Root
		target = GinkgoT().TempDir()
		ctx = ContextWithPromptSources(context.Background(), PromptSources{Dirs: []string{source.Root}})
	})

	writePrompt := func(path string) {
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		Expect(os.WriteFile(path, []byte(validPromptSource), 0o600)).To(Succeed())
	}

	DescribeTable("lists and reads linked files by name, ID and path", func(relative bool) {
		targetPath := filepath.Join(target, "original.prompt")
		writePrompt(targetPath)
		linkPath := filepath.Join(source.Root, "linked.prompt")
		linkTarget := targetPath
		if relative {
			var err error
			linkTarget, err = filepath.Rel(source.Root, targetPath)
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(os.Symlink(linkTarget, linkPath)).To(Succeed())

		listed, err := listPrompts(ctx, PromptListOptions{Source: source.ID})
		Expect(err).NotTo(HaveOccurred())
		Expect(listed).To(HaveLen(1))
		Expect(listed[0].Path).To(Equal(linkPath))
		Expect(listed[0].RelPath).To(Equal("linked.prompt"))
		Expect(listed[0].Writable).To(BeFalse())
		Expect(listed[0].Version).To(Equal(promptVersion(validPromptSource)))
		Expect(listed[0].UpdatedAt).NotTo(BeEmpty())
		for _, id := range []string{"linked", listed[0].ID, linkPath} {
			detail, err := getPrompt(ctx, id)
			Expect(err).NotTo(HaveOccurred())
			Expect(detail.Content).To(Equal(validPromptSource))
			Expect(detail.Writable).To(BeFalse())
			Expect(detail.Version).To(Equal(listed[0].Version))
			Expect(detail.UpdatedAt).To(Equal(listed[0].UpdatedAt))
			rendered, err := renderPrompt(ctx, id, PromptRenderRequest{Variables: map[string]any{"patch": "linked patch"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(rendered.User).To(ContainSubstring("Summarize linked patch."))
		}
	}, Entry("absolute target", false), Entry("relative target", true))

	DescribeTable("rejects explicit non-prompt paths before reading their contents", func(linked bool) {
		path := filepath.Join(target, "prompt.txt")
		writePrompt(path)
		if linked {
			link := filepath.Join(source.Root, "prompt.txt")
			Expect(os.Symlink(path, link)).To(Succeed())
			path = link
		}
		_, err := getPrompt(ctx, path)
		Expect(err).To(MatchError(ContainSubstring("prompt path must end with .prompt")))
	}, Entry("regular file", false), Entry("linked file", true))

	It("includes only immediate regular prompt files from linked directories", func() {
		writePrompt(filepath.Join(target, "review.prompt"))
		writePrompt(filepath.Join(target, "nested", "hidden.prompt"))
		Expect(os.Symlink(filepath.Join(target, "review.prompt"), filepath.Join(target, "alias.prompt"))).To(Succeed())
		Expect(os.Symlink(target, filepath.Join(target, "loop"))).To(Succeed())
		Expect(os.Symlink(target, filepath.Join(source.Root, "shared"))).To(Succeed())
		writePrompt(filepath.Join(source.Root, "ordinary", "deep", "local.prompt"))

		records, err := listPromptRecordsFromSource(source)
		Expect(err).NotTo(HaveOccurred())
		rels := make([]string, 0, len(records))
		for _, record := range records {
			rels = append(rels, record.Rel)
		}
		Expect(rels).To(ConsistOf("shared/review.prompt", "ordinary/deep/local.prompt"))
		id := encodePromptID(source.Kind, source.ID, "shared/review.prompt")
		for _, selector := range []string{"review", id, filepath.Join(source.Root, "shared", "review.prompt")} {
			detail, err := getPrompt(ctx, selector)
			Expect(err).NotTo(HaveOccurred())
			Expect(detail.Content).To(Equal(validPromptSource))
			Expect(detail.Writable).To(BeFalse())
		}
		for _, rel := range []string{"shared/nested/hidden.prompt", "shared/alias.prompt"} {
			_, err := getPrompt(ctx, encodePromptID(source.Kind, source.ID, rel))
			Expect(err).To(HaveOccurred())
		}
	})

	DescribeTable("refuses mutations without changing the link or its target", func(directory bool) {
		targetPath := filepath.Join(target, "review.prompt")
		writePrompt(targetPath)
		linkPath := filepath.Join(source.Root, "linked.prompt")
		rel := "linked.prompt"
		linkTarget := targetPath
		if directory {
			linkPath = filepath.Join(source.Root, "shared")
			linkTarget = target
			rel = "shared/review.prompt"
		}
		Expect(os.Symlink(linkTarget, linkPath)).To(Succeed())
		id := encodePromptID(source.Kind, source.ID, rel)

		_, err := updatePrompt(ctx, id, map[string]any{"content": draftPromptSource})
		Expect(err).To(MatchError(ContainSubstring("read-only")))
		Expect(deletePrompt(ctx, id)).To(MatchError(ContainSubstring("read-only")))
		content, err := os.ReadFile(targetPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(Equal(validPromptSource))
		actualTarget, err := os.Readlink(linkPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(actualTarget).To(Equal(linkTarget))
		copy, err := createPrompt(ctx, map[string]any{"name": "Copy", "content": validPromptSource})
		Expect(err).NotTo(HaveOccurred())
		Expect(copy.Writable).To(BeTrue())
		Expect(copy.Content).To(Equal(validPromptSource))
	}, Entry("file link", false), Entry("directory link", true))

	DescribeTable("reports broken links and cycles even in implicit sources", func(cycle bool) {
		source.Implicit = true
		linkPath := filepath.Join(source.Root, "broken.prompt")
		linkTarget := filepath.Join(target, "missing.prompt")
		if cycle {
			linkTarget = linkPath
		}
		Expect(os.Symlink(linkTarget, linkPath)).To(Succeed())
		_, err := listPromptRecordsFromSource(source)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(linkPath))
	}, Entry("missing target", false), Entry("link cycle", true))

	It("keeps invalid linked prompts readable with parseError", func() {
		targetPath := filepath.Join(target, "broken.prompt")
		Expect(os.WriteFile(targetPath, []byte(brokenPromptSource), 0o600)).To(Succeed())
		Expect(os.Symlink(targetPath, filepath.Join(source.Root, "broken.prompt"))).To(Succeed())
		listed, err := listPrompts(ctx, PromptListOptions{Source: source.ID})
		Expect(err).NotTo(HaveOccurred())
		Expect(listed).To(HaveLen(1))
		Expect(listed[0].ParseError).NotTo(BeEmpty())
		detail, err := getPrompt(ctx, listed[0].ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(detail.Content).To(Equal(brokenPromptSource))
		Expect(detail.ParseError).NotTo(BeEmpty())
	})

	It("reports an unreadable linked target", func() {
		targetPath := filepath.Join(target, "private.prompt")
		writePrompt(targetPath)
		Expect(os.Chmod(targetPath, 0)).To(Succeed())
		DeferCleanup(os.Chmod, targetPath, os.FileMode(0o600))
		linkPath := filepath.Join(source.Root, "private.prompt")
		Expect(os.Symlink(targetPath, linkPath)).To(Succeed())
		_, err := listPrompts(ctx, PromptListOptions{Source: source.ID})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(linkPath))
	})

	It("skips nested links and excluded directories while retaining ordinary recursion", func() {
		writePrompt(filepath.Join(target, "review.prompt"))
		writePrompt(filepath.Join(source.Root, "ordinary", "deep", "local.prompt"))
		Expect(os.Symlink(target, filepath.Join(source.Root, "ordinary", "linked"))).To(Succeed())
		Expect(os.Symlink(target, filepath.Join(source.Root, "node_modules"))).To(Succeed())
		writePrompt(filepath.Join(source.Root, ".git", "hidden.prompt"))
		records, err := listPromptRecordsFromSource(source)
		Expect(err).NotTo(HaveOccurred())
		Expect(records).To(HaveLen(1))
		Expect(records[0].Rel).To(Equal("ordinary/deep/local.prompt"))
		Expect(records[0].Source.Writable).To(BeTrue())
		_, err = getPrompt(ctx, encodePromptID(source.Kind, source.ID, "ordinary/linked/review.prompt"))
		Expect(err).To(HaveOccurred())
	})

	It("refuses creation through a link even when its target is inside the source", func() {
		internal := filepath.Join(source.Root, "internal")
		writePrompt(filepath.Join(internal, "review.prompt"))
		Expect(os.Symlink("internal", filepath.Join(source.Root, "shared"))).To(Succeed())
		_, err := createPrompt(ctx, map[string]any{"relPath": "shared/copy.prompt", "content": validPromptSource})
		Expect(err).To(MatchError(ContainSubstring("read-only")))
		Expect(filepath.Join(internal, "copy.prompt")).NotTo(BeAnExistingFile())
	})

	It("shadows embedded names and reports multiple local aliases as ambiguous", func() {
		targetPath := filepath.Join(target, "original.prompt")
		writePrompt(targetPath)
		Expect(os.Symlink(targetPath, filepath.Join(source.Root, "proposal-review.prompt"))).To(Succeed())
		record, err := resolvePromptRecord(ctx, promptRecordOptions{ID: "proposal-review"})
		Expect(err).NotTo(HaveOccurred())
		Expect(record.Source.Kind).To(Equal("local"))
		writePrompt(filepath.Join(source.Root, "nested", "proposal-review.prompt"))
		_, err = resolvePromptRecord(ctx, promptRecordOptions{ID: "proposal-review"})
		Expect(err).To(MatchError(ContainSubstring("ambiguous")))
	})
})
