package prompt

import (
	"testing/fstest"

	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Declared prompt rendering", func() {
	const source = `---
model: agent:sonnet
temperature: 0.8
effort: high
budget:
  maxTokens: 512
  maxTurns: 3
memory:
  skipUser: false
config:
  temperature: 0
  reasoning: medium
  maxOutputTokens: 128
output:
  schema:
    type: object
    properties:
      summary:
        type: string
---
{{role "system"}}
Inspect {{target}}.
{{role "user"}}
Review {{target}}.
`
	It("preserves authored selection and rendered config, body and output schema", func() {
		spec, cfg, err := Load(source).Render(RenderOptions{Data: map[string]any{"target": "parser.go"}, Declared: true})

		Expect(err).NotTo(HaveOccurred())
		Expect(spec.Model.Name).To(Equal("agent:sonnet"))
		Expect(spec.Model.Provider).To(BeNil())
		Expect(spec.Model.Mode).To(BeEmpty())
		Expect(spec.Temperature).To(HaveValue(Equal(0.0)))
		Expect(spec.Effort).To(Equal(api.EffortMedium))
		Expect(spec.Budget).To(Equal(api.Budget{MaxTokens: 128, MaxTurns: 3}))
		Expect(cfg.Model).To(Equal(spec.Model))
		Expect(cfg.Budget).To(Equal(spec.Budget))
		Expect(spec.Prompt.System).To(Equal("Inspect parser.go."))
		Expect(spec.Prompt.User).To(Equal("Review parser.go."))
		Expect(spec.Prompt.Source).To(Equal("<inline>"))
		Expect(spec.Prompt.SchemaJSON).To(MatchJSON(`{"type":"object","properties":{"summary":{"type":"string"}}}`))
	})

	It("passes the declared option and Go schema target through library rendering", func() {
		target := &struct{ Summary string }{}
		library := NewLibrary(fstest.MapFS{"review.prompt": {Data: []byte(source)}})

		spec, cfg, err := library.Render("review.prompt", RenderOptions{Data: map[string]any{"target": "parser.go"}, Output: target, Declared: true})

		Expect(err).NotTo(HaveOccurred())
		Expect(spec.Model.Name).To(Equal("agent:sonnet"))
		Expect(cfg.Model).To(Equal(spec.Model))
		Expect(spec.Prompt.Schema).To(BeIdenticalTo(target))
		Expect(spec.Prompt.SchemaJSON).To(BeEmpty())
		Expect(spec.Prompt.Source).To(Equal("review.prompt"))
	})

	It("retains explicit zero config values for later composition", func() {
		spec, cfg, err := Load("---\nconfig:\n  maxOutputTokens: 0\n  reasoning: \"\"\n---\nReview\n").Render(RenderOptions{Declared: true})

		Expect(err).NotTo(HaveOccurred())
		Expect(spec.Fields().Has("/budget/maxTokens")).To(BeTrue())
		Expect(spec.Fields().Has("/effort")).To(BeTrue())
		Expect(cfg.Model).To(Equal(spec.Model))
	})
})
