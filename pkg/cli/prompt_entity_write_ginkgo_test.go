package cli

import (
	"context"
	"os"
	"path/filepath"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const validPromptSource = `---
model: claude-sonnet-4-6
description: Summarize a patch
input:
  schema:
    patch: string
---
{{role "user"}}
Summarize {{patch}}.
`

// brokenPromptSource has frontmatter that is not valid YAML, so it cannot be
// rendered or inspected.
const brokenPromptSource = `---
model: [unterminated
---
{{role "user"}}
Broken.
`

const draftPromptSource = `---
model: gpt-5
input:
  schema:
    patch: string
---
{{role "user"}}
Draft body for {{patch}}.
`

var _ = Describe("prompt entity writes", func() {
	var (
		ctx context.Context
		dir string
	)

	BeforeEach(func() {
		captainconfig.SetPathForTesting(filepath.Join(GinkgoT().TempDir(), ".captain.yaml"))
		DeferCleanup(captainconfig.SetPathForTesting, "")
		dir = GinkgoT().TempDir()
		ctx = ContextWithPromptDirs(context.Background(), []string{dir})
	})

	createPrompt := func(name, content string) PromptDetail {
		detail, err := writeNewLocalPrompt(ctx, PromptWriteRequest{Name: name, Content: content})
		Expect(err).NotTo(HaveOccurred())
		return detail
	}

	readFile := func(rel string) string {
		data, err := os.ReadFile(filepath.Join(dir, rel))
		Expect(err).NotTo(HaveOccurred())
		return string(data)
	}

	Describe("create", func() {
		It("validates the content before anything reaches disk", func() {
			_, err := writeNewLocalPrompt(ctx, PromptWriteRequest{Name: "broken", Content: brokenPromptSource})
			Expect(err).To(MatchError(ContainSubstring("invalid prompt")))
			Expect(filepath.Join(dir, "broken.prompt")).NotTo(BeAnExistingFile())
		})

		It("refuses a parent symlink that escapes the prompt source", func() {
			outside := GinkgoT().TempDir()
			Expect(os.Symlink(outside, filepath.Join(dir, "escape"))).To(Succeed())

			_, err := writeNewLocalPrompt(ctx, PromptWriteRequest{
				RelPath: "escape/leaked.prompt",
				Content: validPromptSource,
			})

			Expect(err).To(HaveOccurred())
			Expect(filepath.Join(outside, "leaked.prompt")).NotTo(BeAnExistingFile())
		})

		It("returns the content version of the new prompt", func() {
			detail := createPrompt("summary", validPromptSource)
			Expect(detail.Version).To(Equal(promptVersion(validPromptSource)))
			Expect(detail.UpdatedAt).NotTo(BeEmpty())
		})
	})

	Describe("update", func() {
		It("rejects invalid content and leaves the file byte-identical", func() {
			detail := createPrompt("summary", validPromptSource)

			_, err := updatePrompt(ctx, detail.ID, map[string]any{"content": brokenPromptSource})

			Expect(err).To(MatchError(ContainSubstring("invalid prompt")))
			Expect(readFile("summary.prompt")).To(Equal(validPromptSource))
		})

		It("refuses a stale base version so an external edit is not clobbered", func() {
			detail := createPrompt("summary", validPromptSource)
			external := validPromptSource + "\nEdited outside the UI.\n"
			Expect(os.WriteFile(filepath.Join(dir, "summary.prompt"), []byte(external), 0o644)).To(Succeed())

			_, err := updatePrompt(ctx, detail.ID, map[string]any{
				"content":     draftPromptSource,
				"baseVersion": detail.Version,
			})

			Expect(err).To(MatchError(ContainSubstring("changed on disk")))
			Expect(readFile("summary.prompt")).To(Equal(external))
		})

		It("writes when the base version matches and reports the new version", func() {
			detail := createPrompt("summary", validPromptSource)

			updated, err := updatePrompt(ctx, detail.ID, map[string]any{
				"content":     draftPromptSource,
				"baseVersion": detail.Version,
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(readFile("summary.prompt")).To(Equal(draftPromptSource))
			Expect(updated.Version).To(Equal(promptVersion(draftPromptSource)))
			Expect(updated.Version).NotTo(Equal(detail.Version))
			Expect(updated.Model).To(Equal("gpt-5"))
		})
	})

	Describe("get", func() {
		It("returns a repairable detail for a prompt that no longer parses", func() {
			detail := createPrompt("summary", validPromptSource)
			Expect(os.WriteFile(filepath.Join(dir, "summary.prompt"), []byte(brokenPromptSource), 0o644)).To(Succeed())

			broken, err := getPrompt(ctx, detail.ID)

			Expect(err).NotTo(HaveOccurred())
			Expect(broken.ParseError).NotTo(BeEmpty())
			Expect(broken.Content).To(Equal(brokenPromptSource))
			Expect(broken.Version).To(Equal(promptVersion(brokenPromptSource)))
			Expect(broken.Name).To(Equal("summary"))
		})

		It("lists and details the same version and timestamp", func() {
			detail := createPrompt("summary", validPromptSource)

			prompts, err := listPrompts(ctx, PromptListOptions{Source: "local"})

			Expect(err).NotTo(HaveOccurred())
			Expect(prompts).To(HaveLen(1))
			Expect(prompts[0].Version).To(Equal(detail.Version))
			Expect(prompts[0].UpdatedAt).To(Equal(detail.UpdatedAt))
		})
	})

	Describe("render", func() {
		It("renders the draft model when the sparse request has no model override", func() {
			detail := createPrompt("summary", validPromptSource)

			result, err := renderPrompt(ctx, detail.ID, PromptRenderRequest{
				Content:   draftPromptSource,
				Variables: map[string]any{"patch": "abc"},
				Spec:      &api.Spec{},
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(result.User).To(ContainSubstring("Draft body for abc."))
			Expect(result.Model).To(Equal("gpt-5"))
		})

		It("preserves an explicit model equal to the saved prompt when rendering a draft", func() {
			detail := createPrompt("summary", validPromptSource)
			result, err := renderPrompt(ctx, detail.ID, PromptRenderRequest{
				Content:   draftPromptSource,
				Variables: map[string]any{"patch": "abc"},
				Spec:      &api.Spec{Model: api.Model{Name: detail.Model, Mode: api.RuntimeMode(detail.Mode)}},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Model).To(Equal("claude-sonnet-4-6"))
		})

		It("keeps an explicit runtime override when rendering a draft", func() {
			detail := createPrompt("summary", validPromptSource)

			result, err := renderPrompt(ctx, detail.ID, PromptRenderRequest{
				Content:   draftPromptSource,
				Variables: map[string]any{"patch": "abc"},
				Spec:      &api.Spec{Model: api.Model{Name: "claude-opus-4-6", Mode: api.ModeAPI}},
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(result.Model).To(Equal("claude-opus-4-6"))
		})

		It("still renders the saved file when no draft is sent", func() {
			detail := createPrompt("summary", validPromptSource)

			result, err := renderPrompt(ctx, detail.ID, PromptRenderRequest{
				Variables: map[string]any{"patch": "abc"},
				Spec:      detail.Run.Spec,
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(result.User).To(ContainSubstring("Summarize abc."))
			Expect(result.Model).To(Equal("claude-sonnet-4-6"))
		})
	})
})
