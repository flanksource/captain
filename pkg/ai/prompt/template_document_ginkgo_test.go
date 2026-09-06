package prompt

import (
	"testing"
	"testing/fstest"

	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestPromptDocuments(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Prompt documents")
}

var _ = Describe("Template document", func() {
	DescribeTable("preserves rendered authored metadata without resolving driver defaults",
		func(frontmatter string, data map[string]any, model api.Model) {
			const body = "{{role \"user\"}}\nReview {{name}}\n"
			document, err := Load("---\n" + frontmatter + "---\n" + body).Document(data)
			Expect(err).NotTo(HaveOccurred())
			Expect(document.RuntimeProfile).To(Equal("reviewer"))
			Expect(document.Spec).To(Equal(api.Spec{Model: model}))
			Expect(document.Body).To(Equal(body))
			Expect(document.Frontmatter).To(HaveKeyWithValue("runtimeProfile", "reviewer"))
			Expect(document.Frontmatter).To(HaveKeyWithValue("model", model.Name))
		},
		Entry("static fields with no authored mode", "runtimeProfile: reviewer\nmodel: sonnet\n", nil, api.Model{Name: "sonnet"}),
		Entry("static compact selector", "runtimeProfile: reviewer\nmodel: agent:sonnet\n", nil, api.Model{Name: "agent:sonnet"}),
		Entry("templated profile, model and mode", "runtimeProfile: {{profile}}\nmodel: {{model}}\nmode: {{mode}}\n",
			map[string]any{"profile": "reviewer", "model": "sonnet", "mode": "cli", "name": "a change"},
			api.Model{Name: "sonnet", Mode: api.ModeCLI}),
		Entry("unknown catalog model remains authored", "runtimeProfile: reviewer\nmodel: tenant-review-model\nmode: agent\n",
			nil, api.Model{Name: "tenant-review-model", Mode: api.ModeAgent}),
	)

	It("returns a body-only prompt without introducing metadata", func() {
		const body = "Review {{name}}\n"
		document, err := Load(body).Document(map[string]any{"name": "a change"})
		Expect(err).NotTo(HaveOccurred())
		Expect(document).To(Equal(&Document{Body: body}))
	})

	DescribeTable("reports malformed frontmatter with its source name",
		func(frontmatter string, data map[string]any, message string) {
			const name = "prompts/review.prompt"
			template, err := LoadFS(fstest.MapFS{name: {Data: []byte("---\n" + frontmatter + "---\nbody\n")}}, name)
			Expect(err).NotTo(HaveOccurred())
			document, err := template.Document(data)
			Expect(document).To(BeNil())
			Expect(err).To(MatchError(ContainSubstring(name)))
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("invalid YAML", "model: [unfinished\n", nil, "parse prompt frontmatter"),
		Entry("invalid template", "{{#if enabled}}\nmodel: sonnet\n", nil, "frontmatter template"),
		Entry("invalid rendered YAML", "model: {{model}}\n", map[string]any{"model": "[unfinished"}, "parse prompt frontmatter"),
		Entry("invalid rendered profile", "runtimeProfile: {{profile}}\n", map[string]any{"profile": 42}, "runtimeProfile must be a string"),
	)
})
