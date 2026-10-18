package prompt

import (
	"encoding/json"

	"github.com/flanksource/captain/pkg/ai/jev"
	"github.com/flanksource/captain/pkg/api"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Jev commit classifier fixture", func() {
	It("preserves native TypeSafe questions without converting them as Picoschema", func() {
		source := `---
model: jev-latest
input:
  schemaType: json-schema
  schema:
    type: object
    properties:
      message:
        type: string
output:
  schemaType: typesafe-schema
  schema:
    valid:
      type: noul
      instructions: Is this commit valid?
    risk:
      type: score
      instructions: How risky is this commit?
      criteria: [Low, Medium, High]
---
{{message}}`
		req, _, err := Load(source).Render(RenderOptions{Data: map[string]any{"message": "feat: add Jev"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(req.Prompt.User).To(Equal("feat: add Jev"))
		Expect(req.Prompt.InputSchemaType).To(Equal(api.SchemaTypeJSONSchema))
		Expect(req.Prompt.OutputSchemaType).To(Equal(api.SchemaTypeTypeSafe))
		Expect(req.Prompt.SchemaJSON).To(BeEmpty())
		Expect(req.Prompt.TypeSafeQuestions).To(MatchJSON(`{"valid":{"type":"noul","instructions":"Is this commit valid?"},"risk":{"type":"score","instructions":"How risky is this commit?","criteria":["Low","Medium","High"]}}`))
	})
	It("rejects invalid native question criteria during prompt rendering", func() {
		source := `---
model: jev-latest
output:
  schemaType: typesafe-schema
  schema:
    confidence:
      type: score
      instructions: How confident?
      criteria: [Only one level]
---
Review this commit.`
		_, _, err := Load(source).Render(RenderOptions{})
		Expect(err).To(MatchError(ContainSubstring("score criteria must contain 2 to 10 ordered levels")))
	})

	It("renders commit evidence with native questions for every classification", func() {
		tmpl, err := LoadFS(library, "testdata/fixtures/jev.prompt")
		Expect(err).NotTo(HaveOccurred())

		req, _, err := tmpl.Render(RenderOptions{Data: map[string]any{
			"targetScope": "provider integrations",
			"message":     "feat(jev): add enum output",
			"patch":       "diff --git a/provider.go b/provider.go\n+registerJev()",
		}})
		Expect(err).NotTo(HaveOccurred())
		Expect(req.Prompt.System).To(ContainSubstring("Classify the Git commit"))
		Expect(req.Prompt.User).To(SatisfyAll(
			ContainSubstring("provider integrations"),
			ContainSubstring("feat(jev): add enum output"),
			ContainSubstring("+registerJev()"),
		))

		Expect(req.Prompt.InputSchemaType).To(Equal(api.SchemaTypeJSONSchema))
		Expect(req.Prompt.OutputSchemaType).To(Equal(api.SchemaTypeTypeSafe))
		var questions map[string]jev.Question
		Expect(json.Unmarshal(req.Prompt.TypeSafeQuestions, &questions)).To(Succeed())
		Expect(questions).To(HaveLen(7))
		for name, kind := range map[string]string{
			"input_valid": "noul", "in_scope": "choice", "commit_type": "choice", "change_type": "choice",
			"risk": "choice", "breaking_change": "choice", "confidence": "score",
		} {
			Expect(questions[name].Type).To(Equal(kind), name)
			Expect(questions[name].Instructions).NotTo(BeEmpty(), name)
			Expect(questions[name].Validate()).To(Succeed(), name)
		}
		Expect(questions["confidence"].Criteria).To(MatchJSON(`[
			"Low confidence because key evidence is missing, conflicting, or invalid.",
			"Medium confidence because some evidence is ambiguous.",
			"High confidence because the message and patch agree and evidence is complete."]`))
	})
})
