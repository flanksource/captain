package cli

import (
	"context"
	"path/filepath"
	"testing/fstest"

	"github.com/flanksource/captain/pkg/captainconfig"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("host fs.FS prompt sources", func() {
	var (
		ctx  context.Context
		fsys fstest.MapFS
	)

	BeforeEach(func() {
		captainconfig.SetPathForTesting(filepath.Join(GinkgoT().TempDir(), ".captain.yaml"))
		DeferCleanup(captainconfig.SetPathForTesting, "")
		fsys = fstest.MapFS{"prompts/branch-merge.prompt": {Data: []byte(validPromptSource)}}
		ctx = ContextWithPromptSources(context.Background(), PromptSources{
			Dirs: []string{GinkgoT().TempDir()},
			FS:   []PromptFS{{Label: "gavel", FS: fsys}},
		})
	})

	It("resolves a bare name from the host's source as a read-only prompt", func() {
		record, err := resolvePromptRecord(ctx, promptRecordOptions{ID: "branch-merge"})
		Expect(err).NotTo(HaveOccurred())
		Expect(record.Source.Kind).To(Equal("embedded"))
		Expect(record.Source.Label).To(Equal("gavel"))
		Expect(record.Source.Writable).To(BeFalse())
		Expect(record.Rel).To(Equal("prompts/branch-merge.prompt"))

		detail, err := getPrompt(ctx, record.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(detail.Content).To(Equal(validPromptSource))
		Expect(detail.Description).To(Equal("Summarize a patch"))
	})

	It("lists the host's prompts under its label and refuses to edit them", func() {
		prompts, err := listPrompts(ctx, PromptListOptions{Source: "gavel"})
		Expect(err).NotTo(HaveOccurred())
		Expect(prompts).To(HaveLen(1))
		Expect(prompts[0].Name).To(Equal("branch-merge"))
		Expect(prompts[0].Writable).To(BeFalse())

		_, err = updatePrompt(ctx, prompts[0].ID, map[string]any{"content": validPromptSource})
		Expect(err).To(MatchError(ContainSubstring("read-only")))
	})

	DescribeTable("refuses a source it could not address",
		func(sources []PromptFS, want string) {
			bad := ContextWithPromptSources(context.Background(), PromptSources{FS: sources})
			_, err := resolvePromptRecord(bad, promptRecordOptions{ID: "branch-merge"})
			Expect(err).To(MatchError(ContainSubstring(want)))
		},
		Entry("blank label", []PromptFS{{Label: " ", FS: fstest.MapFS{}}}, "label is required"),
		Entry("nil fs", []PromptFS{{Label: "gavel"}}, `prompt source "gavel" has no fs.FS`),
		Entry("label shadowing captain's embedded examples", []PromptFS{{Label: "Embedded", FS: fstest.MapFS{}}}, "already registered"),
		Entry("duplicate label", []PromptFS{{Label: "gavel", FS: fstest.MapFS{}}, {Label: "gavel", FS: fstest.MapFS{}}}, "already registered"),
	)
})
