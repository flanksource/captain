package cli

import (
	"testing"

	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestPromptSchemaTypes(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Prompt Schema Types Suite")
}

var _ = Describe("Prompt schema inspection", func() {
	It("exposes native TypeSafe questions and explicit input and output types", func() {
		content := `---
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
      instructions: Is this input valid?
---
{{message}}`
		inspection, err := inspectPrompt(content, map[string]any{"message": "feat: example"})
		Expect(err).NotTo(HaveOccurred())
		Expect(inspection.InputSchemaType).To(Equal(api.SchemaTypeJSONSchema))
		Expect(inspection.OutputSchemaType).To(Equal(api.SchemaTypeTypeSafe))
		Expect(inspection.OutputSchema).To(HaveKeyWithValue("valid", map[string]any{
			"type": "noul", "instructions": "Is this input valid?",
		}))
	})
})
