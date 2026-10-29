package plans_test

import (
	"path/filepath"

	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/plans"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("plan hook discovery parsing", func() {
	It("discovers hooks beside a prompt whose frontmatter is templated", func() {
		dir := GinkgoT().TempDir()
		writePrompt(dir, "commit-grouping", "output:\n  schema:\n    minItems: 1\n{{#if maxCommits}}\n    maxItems: {{maxCommits}}\n{{/if}}")
		writePrompt(dir, notesHook, "model: api:haiku\nhook: {on: plan.revision, mode: append}")

		hooks, err := plans.DiscoverHooks(dir)

		Expect(err).NotTo(HaveOccurred())
		Expect(hookNames(hooks)).To(Equal(map[string]promptlib.HookMode{
			tldrHook: promptlib.HookPrepend, notesHook: promptlib.HookAppend,
		}))
	})

	DescribeTable("fails naming the source of a prompt whose frontmatter is malformed",
		func(frontmatter string) {
			dir := GinkgoT().TempDir()
			writePrompt(dir, "a-broken", frontmatter)

			_, err := plans.DiscoverHooks(dir)

			Expect(err).To(MatchError(SatisfyAll(
				ContainSubstring(dir+":a-broken.prompt"),
				ContainSubstring("parse prompt a-broken.prompt frontmatter"),
			)))
		},
		Entry("malformed YAML", "model: [unfinished"),
		Entry("malformed hook declaration", "hook: [unfinished"),
		Entry("unterminated template block", "model: x\n{{#if open}}"),
	)

	It("continues to return filesystem discovery failures", func() {
		_, err := plans.DiscoverHooks(filepath.Join(GinkgoT().TempDir(), "missing"))
		Expect(err).To(HaveOccurred())
	})
})
