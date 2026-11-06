package api

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

var _ = Describe("Prompt YAML schemas", func() {
	It("decodes raw schema objects in reusable preset YAML without losing numeric constraints", func() {
		var spec RuntimePresetSpec
		Expect(yaml.Unmarshal([]byte("prompt:\n  schemaJSON:\n    type: object\n    properties:\n      count:\n        type: integer\n        maximum: 9007199254740993\n  inputSchemaJSON:\n    type: object\n  typeSafeQuestions: null\n"), &spec)).To(Succeed())
		Expect(spec.Prompt.SchemaJSON).To(MatchJSON(`{"type":"object","properties":{"count":{"type":"integer","maximum":9007199254740993}}}`))
		Expect(spec.Prompt.InputSchemaJSON).To(MatchJSON(`{"type":"object"}`))
		Expect(spec.ToSpec().Fields()).To(HaveKey("/prompt/typeSafeQuestions"))
		encoded, err := yaml.Marshal(spec)
		Expect(err).NotTo(HaveOccurred())
		var again RuntimePresetSpec
		Expect(yaml.Unmarshal(encoded, &again)).To(Succeed())
		Expect(json.Valid(again.Prompt.SchemaJSON)).To(BeTrue())
		Expect(again.Prompt.SchemaJSON).To(MatchJSON(spec.Prompt.SchemaJSON))
	})
})
