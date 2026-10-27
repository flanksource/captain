package cli

import (
	"context"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/runtimeprofiles"
	"path/filepath"
	"testing/fstest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("prepared prompt inputs", func() {
	BeforeEach(func() {
		captainconfig.SetPathForTesting(filepath.Join(GinkgoT().TempDir(), "captain.yaml"))
		DeferCleanup(func() { captainconfig.SetPathForTesting("") })
	})
	It("discovers undeclared inputs through Captain's template parser", func() {
		schema, err := PromptFormSchema(PromptDetail{Content: "Review {{input}} {{#if files}}in {{files}}{{/if}}"})
		Expect(err).NotTo(HaveOccurred())
		Expect(schema["properties"]).To(Equal(map[string]any{"input": map[string]any{"type": "string"}, "files": map[string]any{"type": "string"}}))
	})
	It("validates input types after applying defaults without changing supplied values", func() {
		detail := PromptDetail{InputSchema: map[string]any{"type": "object", "required": []any{"count"}, "properties": map[string]any{"count": map[string]any{"type": "integer", "default": 3}}}}
		values, err := validatePromptVariables(detail, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(values["count"]).To(BeNumerically("==", 3))
		_, err = validatePromptVariables(detail, map[string]any{"count": "wrong"})
		Expect(err).To(HaveOccurred())
	})
	It("rejects a stale version before rendering", func() {
		files := fstest.MapFS{"review.prompt": &fstest.MapFile{Data: []byte("---\nmodel: mock/test\ninput:\n  schema:\n    subject: string\n---\nReview {{subject}}")}}
		ctx := ContextWithPromptSources(context.Background(), PromptSources{FS: []PromptFS{{Label: "prepared-test", FS: files}}})
		directory := GinkgoT().TempDir()
		detail, err := InspectPrompt(ctx, directory, "review")
		Expect(err).NotTo(HaveOccurred())
		_, err = PreparePrompt(ctx, PreparePromptOptions{Reference: detail.ID, Version: "stale", Directory: directory})
		Expect(err).To(MatchError(ContainSubstring(ErrPromptVersion.Error())))
	})
	It("freezes accepted content and variables before the prompt file changes", func() {
		files := fstest.MapFS{"frozen.prompt": &fstest.MapFile{Data: []byte("---\nmodel: api:sonnet\ninput:\n  schema:\n    subject: string\n---\nReview {{subject}}")}}
		ctx := ContextWithPromptSources(context.Background(), PromptSources{FS: []PromptFS{{Label: "frozen-test", FS: files}}})
		catalog, err := runtimeprofiles.NewCatalog(runtimeprofiles.NewBuiltinSource())
		Expect(err).NotTo(HaveOccurred())
		ctx = ContextWithRuntimeCatalog(ctx, catalog)
		directory := GinkgoT().TempDir()
		detail, err := InspectPrompt(ctx, directory, "frozen")
		Expect(err).NotTo(HaveOccurred())
		variables := map[string]any{"subject": "the accepted design"}
		prepared, err := PreparePrompt(ctx, PreparePromptOptions{Reference: detail.ID, Version: detail.Version, Directory: directory, Request: PromptRenderRequest{Variables: variables}})
		Expect(err).NotTo(HaveOccurred())
		files["frozen.prompt"].Data = []byte("Changed {{subject}}")
		variables["subject"] = "changed variable"
		Expect(prepared.rendered.Input.Prompt.User).To(Equal("Review the accepted design"))
		Expect(prepared.Detail().Version).To(Equal(detail.Version))
	})
})
