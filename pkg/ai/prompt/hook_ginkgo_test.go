package prompt

import (
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// templatedSchema is frontmatter that is YAML only once rendered, like gavel's
// commit-grouping prompt capping its groups with an optional maxCommits.
const templatedSchema = "output:\n  schema:\n    type: array\n    minItems: 1\n{{#if maxCommits}}\n    maxItems: {{maxCommits}}\n{{/if}}"

func hookSource(hook string) *Template {
	return LoadNamed("hook.prompt", "---\n"+hook+"\n---\n{{role \"user\"}}\nbody\n")
}

var _ = Describe("HookSpec", func() {
	It("parses match, when and render alongside the event and mode", func() {
		spec, err := hookSource("hook:\n  on: prompt.context\n  mode: substitute\n  match: '#(?P<num>\\d+)'\n  when: has(metadata.pr)\n  render: model\n  field: text").Hook()
		Expect(err).NotTo(HaveOccurred())
		Expect(*spec).To(Equal(HookSpec{
			On: "prompt.context", Mode: HookSubstitute, Field: "text",
			Match: `#(?P<num>\d+)`, When: "has(metadata.pr)", Render: HookRenderModel,
		}))
	})

	It("reads no hook from templated frontmatter that declares none", func() {
		spec, err := hookSource(templatedSchema).Hook()
		Expect(err).NotTo(HaveOccurred())
		Expect(spec).To(BeNil())
	})

	It("reads the hook beside templated frontmatter, keeping hook-root expressions", func() {
		spec, err := hookSource(templatedSchema + "\nhook:\n  on: prompt.context\n  mode: append\n  when: '\"{{metadata.pr}}\" != \"\"'").Hook()
		Expect(err).NotTo(HaveOccurred())
		Expect(*spec).To(Equal(HookSpec{On: "prompt.context", Mode: HookAppend, When: `"{{metadata.pr}}" != ""`}))
	})

	It("reports malformed frontmatter as a HookParseError", func() {
		_, err := LoadNamed("broken.prompt", "---\nhook: [unfinished\n---\nbody\n").Hook()
		var parseErr *HookParseError
		Expect(errors.As(err, &parseErr)).To(BeTrue())
		Expect(parseErr.Prompt).To(Equal("broken.prompt"))
	})

	DescribeTable("rejects an invalid hook",
		func(hook, message string) {
			_, err := hookSource(hook).Hook()
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("substitute without match", "hook: {on: prompt.context, mode: substitute, when: 'true'}", "substitute requires `match`"),
		Entry("unknown render", "hook: {on: prompt.context, mode: append, when: 'true', render: shell}", `render "shell"`),
		Entry("unknown mode", "hook: {on: prompt.context, mode: overwrite}", `mode "overwrite"`),
	)
})

var _ = Describe("HookBlocks", func() {
	const (
		prefix   = "captain:test"
		name     = "pr"
		body     = "see PR #12 now"
		span     = `#12 "quoted" -->`
		output   = "PR 12 is green"
		appended = "extra context"
	)
	blocks := NewHookBlocks(prefix)

	It("wraps output in named markers and finds it again", func() {
		text := body + "\n\n" + blocks.Wrap(name, appended)
		Expect(text).To(Equal(body + "\n\n<!-- captain:test:pr -->\n" + appended + "\n<!-- /captain:test:pr -->"))
		Expect(blocks.Find(text)).To(Equal(map[string]string{name: appended}))
		Expect(blocks.Strip(text)).To(Equal(body))
	})

	It("restores the span a substituting block replaced when stripped", func() {
		text := "see PR " + blocks.WrapReplacing(name, output, span) + " now"
		Expect(blocks.Find(text)).To(Equal(map[string]string{name: output}))
		Expect(blocks.Strip(text)).To(Equal("see PR " + span + " now"))
	})

	It("leaves blocks of another prefix and mismatched markers alone", func() {
		other := NewHookBlocks("captain:other").Wrap(name, appended)
		mismatched := "<!-- captain:test:a -->\nx\n<!-- /captain:test:b -->"
		Expect(blocks.Strip(other + "\n" + mismatched)).To(Equal(other + "\n" + mismatched))
	})
})
