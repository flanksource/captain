package prompt

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

const hookedPrompt = `---
model: jev-latest
mode: api
tools:
  choices:
    description: Emit product choices
    readonly: true
    exec: emit-choices --product {{product}}
  record:
    description: Record the answer
    exec: cat
output:
  schemaType: typesafe-schema
  hooks:
    - tool: choices
      with: {product: "{{vars.product}}"}
  schema:
    product:
      type: choice
      instructions: Which product?
      criteria: {unknown: No evidence.}
workflow:
  after:
    - when: onSuccess
      tool: record
      with: {values: "{{output.value}}", encoded: "{{json output.value}}", file: "{{vars.file}}"}
    - when: onSuccess
      if: output.value.product != "unknown"
      prompt: stage2.prompt
      with: {product: "{{output.value.product}}"}
---
Classify {{product}}.
`

const inputHookedPrompt = `---
model: jev-latest
mode: api
tools:
  load:
    description: Load the issue
    readonly: true
    exec: cat
  record:
    description: Record the answer
    exec: cat
input:
  hooks:
    - tool: load
      with: {path: "{{vars.file}}"}
    - if: vars.product == "Annuity"
      prompt: stage2.prompt
---
Classify {{issue}}.
`

var _ = Describe("Hook frontmatter", func() {
	data := map[string]any{"product": "Annuity", "file": "/issues/KEY-1.md"}

	It("decodes output.hooks onto the prompt and leaves hook expressions for the hooks to render", func() {
		dir := GinkgoT().TempDir()
		req, _, err := LoadNamed("classify.prompt", hookedPrompt).Render(RenderOptions{Data: data, Dir: dir})
		Expect(err).NotTo(HaveOccurred())

		Expect(req.Prompt.SchemaHooks).To(Equal([]api.HookAction{{Tool: "choices", With: map[string]any{"product": "{{vars.product}}"}}}))
		Expect(req.Tools["choices"].Exec).To(Equal("emit-choices --product Annuity"), "exec is rendered with the run's vars")
		Expect(req.Workflow.After[0].With).To(Equal(map[string]any{
			"values": "{{output.value}}", "encoded": "{{json output.value}}", "file": "{{vars.file}}",
		}))
		Expect(req.Workflow.After[1].Prompt).To(Equal(filepath.Join(dir, "stage2.prompt")), "a child prompt resolves against the parent's directory")
		Expect(req.Prompt.Variables).To(Equal(data))
		Expect(req.Prompt.TypeSafeQuestions).To(MatchJSON(`{"product":{"type":"choice","instructions":"Which product?","criteria":{"unknown":"No evidence."}}}`))
	})

	It("refuses a relative child prompt when the template has no directory to resolve it against", func() {
		_, _, err := LoadNamed("classify.prompt", hookedPrompt).Render(RenderOptions{Data: data})
		Expect(err).To(MatchError(ContainSubstring(`workflow.after[1].prompt "stage2.prompt" is relative, but the prompt was not loaded from a directory`)))
	})

	emittedOnly := strings.Replace(hookedPrompt,
		"  schema:\n    product:\n      type: choice\n      instructions: Which product?\n      criteria: {unknown: No evidence.}\n",
		"  schema: {}\n", 1)

	It("starts an empty TypeSafe schema for its output.hooks to emit", func() {
		req, _, err := LoadNamed("classify.prompt", emittedOnly).Render(RenderOptions{Data: data, Dir: GinkgoT().TempDir()})
		Expect(err).NotTo(HaveOccurred())
		Expect(req.Prompt.TypeSafeQuestions).To(MatchJSON(`{}`))
		Expect(req.Prompt.Validate()).To(Succeed())
	})

	It("still refuses an empty TypeSafe schema that no hook emits", func() {
		noHooks := strings.Replace(emittedOnly, "  hooks:\n    - tool: choices\n      with: {product: \"{{vars.product}}\"}\n", "", 1)
		_, _, err := LoadNamed("classify.prompt", noHooks).Render(RenderOptions{Data: data, Dir: GinkgoT().TempDir()})
		Expect(err).To(MatchError(ContainSubstring("output.schema must contain TypeSafe questions")))
	})

	It("decodes input.hooks and resolves their child prompts beside the parent", func() {
		dir := GinkgoT().TempDir()
		req, _, err := LoadNamed("classify.prompt", inputHookedPrompt).Render(RenderOptions{Data: data, Dir: dir})
		Expect(err).NotTo(HaveOccurred())

		Expect(req.Prompt.InputHooks).To(Equal([]api.HookAction{
			{Tool: "load", With: map[string]any{"path": "{{vars.file}}"}},
			{If: `vars.product == "Annuity"`, Prompt: filepath.Join(dir, "stage2.prompt")},
		}))
		Expect(req.Prompt.Variables).To(Equal(data))
	})

	It("keeps no variables on a spec that declares no hooks", func() {
		req, _, err := LoadNamed("plain.prompt", "---\nmodel: jev-latest\n---\nHello {{name}}").Render(RenderOptions{Data: map[string]any{"name": "x"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(req.Prompt.Variables).To(BeNil())
	})

	Context("lint", func() {
		write := func(source string) *Template {
			dir := GinkgoT().TempDir()
			Expect(os.WriteFile(filepath.Join(dir, "stage2.prompt"), []byte("---\nmodel: jev-latest\n---\nhi"), 0o644)).To(Succeed())
			path := filepath.Join(dir, "classify.prompt")
			Expect(os.WriteFile(path, []byte(source), 0o644)).To(Succeed())
			template, err := LoadFile(path)
			Expect(err).NotTo(HaveOccurred())
			return template
		}
		errorsOf := func(template *Template) []string {
			var messages []string
			for _, diagnostic := range template.Validate(ValidationOptions{Data: data}) {
				messages = append(messages, diagnostic.Severity+": "+diagnostic.Message)
			}
			return messages
		}

		It("accepts the hooked prompt without deferring on hook-only variables", func() {
			Expect(errorsOf(write(hookedPrompt))).To(BeEmpty())
		})

		It("accepts an empty schema that output.hooks emit", func() {
			Expect(errorsOf(write(emittedOnly))).To(BeEmpty())
		})

		DescribeTable("rejects a hook that cannot run",
			func(from, to, want string) {
				Expect(errorsOf(write(strings.Replace(hookedPrompt, from, to, 1)))).To(ContainElement(ContainSubstring(want)))
			},
			Entry("unknown tool", "- tool: choices", "- tool: missing", `output.hooks[0].tool "missing" is not declared in tools`),
			Entry("missing prompt file", "prompt: stage2.prompt", "prompt: stage3.prompt", "workflow.after[1].prompt"),
			Entry("bad CEL", `if: output.value.product != "unknown"`, `if: output.value.product !=`, `if "output.value.product !="`),
			Entry("two actions", "      tool: record\n", "      tool: record\n      prompt: stage2.prompt\n", "declares 2 actions"),
		)

		It("accepts readonly input hooks", func() {
			Expect(errorsOf(write(inputHookedPrompt))).To(BeEmpty())
		})

		DescribeTable("rejects an input hook that cannot run",
			func(from, to, want string) {
				Expect(errorsOf(write(strings.Replace(inputHookedPrompt, from, to, 1)))).To(ContainElement(ContainSubstring(want)))
			},
			Entry("non-readonly tool", "    - tool: load", "    - tool: record", `input.hooks[0].tool "record" is not readonly`),
			Entry("missing prompt file", "prompt: stage2.prompt", "prompt: stage3.prompt", "input.hooks[1].prompt"),
		)
	})
})
