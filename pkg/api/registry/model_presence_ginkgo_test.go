package registry

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

var _ = Describe("authored model field presence", func() {
	DescribeTable("preserves explicit zero values across decoding and encoding", func(input string, decode func([]byte, any) error, encode func(any) ([]byte, error)) {
		var model Model
		Expect(decode([]byte(input), &model)).To(Succeed())
		Expect(model.Explicit).To(HaveKeyWithValue("/noCache", true))
		Expect(model.Explicit).To(HaveKeyWithValue("/fallbacks", true))
		Expect(model.Temperature).NotTo(BeNil())
		Expect(*model.Temperature).To(BeZero())
		encoded, err := encode(model)
		Expect(err).NotTo(HaveOccurred())
		var roundtrip Model
		Expect(decode(encoded, &roundtrip)).To(Succeed())
		Expect(roundtrip).To(Equal(model))
	},
		Entry("JSON", `{"noCache":false,"temperature":0,"fallbacks":[]}`, json.Unmarshal, json.Marshal),
		Entry("YAML", "noCache: false\ntemperature: 0\nfallbacks: []\n", yaml.Unmarshal, yaml.Marshal),
	)

	It("lets explicit false and an empty list replace inherited values without mutating inputs", func() {
		base := Model{NoCache: true, Fallbacks: ModelList{{Name: "agent:sonnet"}}}
		override := Model{Explicit: FieldPresence{"/noCache": true, "/fallbacks": true}, Fallbacks: ModelList{}}
		merged := base.Merge(override)
		Expect(merged.NoCache).To(BeFalse())
		Expect(merged.Fallbacks).To(BeEmpty())
		Expect(base.NoCache).To(BeTrue())
		Expect(base.Fallbacks).To(HaveLen(1))
		merged.Explicit["/mode"] = true
		Expect(override.Explicit).NotTo(HaveKey("/mode"))
	})

	It("keeps raw compact fallback selectors and nested explicit false on the wire", func() {
		var models ModelList
		Expect(json.Unmarshal([]byte(`["agent:sonnet:high",{"model":"api:haiku","noCache":false}]`), &models)).To(Succeed())
		Expect(models[0].Name).To(Equal("agent:sonnet:high"))
		Expect(models[0].Mode).To(BeEmpty())
		Expect(models[1].Explicit).To(HaveKeyWithValue("/noCache", true))
		encoded, err := json.Marshal(models)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(encoded)).To(ContainSubstring(`"noCache":false`))
	})

	It("preserves an explicitly cacheable fallback when generating execution candidates", func() {
		model := Model{Name: "agent:sonnet", NoCache: true, Fallbacks: ModelList{{Name: "api:haiku", Explicit: FieldPresence{"/noCache": true}}}}
		Expect(model.Candidates()[1].NoCache).To(BeFalse())
	})

	It("preserves explicit empty fallback knobs when generating execution candidates", func() {
		temperature := 0.6
		model := Model{Name: "sonnet", Mode: ModeAgent, Effort: EffortHigh, Temperature: &temperature,
			Fallbacks: ModelList{(Model{Name: "haiku", Mode: ModeAgent}).WithExplicit("/effort", "/temperature")},
		}
		Expect(model.Candidates()[1].Effort).To(BeEmpty())
		Expect(model.Candidates()[1].Temperature).To(BeNil())
	})

	It("preserves zero values inherited through YAML merge keys", func() {
		var model Model
		Expect(yaml.Unmarshal([]byte("<<: &defaults {noCache: false, temperature: 0}\nmodel: sonnet\n"), &model)).To(Succeed())
		Expect(model.Explicit).To(HaveKeyWithValue("/noCache", true))
		Expect(model.Explicit).To(HaveKeyWithValue("/temperature", true))
		Expect(model.Explicit).NotTo(HaveKey("/<<"))
	})

	It("tracks only model fields when decoding unknown or outer object fields", func() {
		var model Model
		Expect(json.Unmarshal([]byte(`{"model":"sonnet","backend":"cli","budget":{"cost":0}}`), &model)).To(Succeed())
		Expect(model.Explicit).To(Equal(FieldPresence{"/model": true}))
	})

	It("normalizes authored effort only at final resolution for primary and fallback", func() {
		model := Model{Name: "agent:sonnet:ultra", Fallbacks: ModelList{{Name: "api:sonnet:ultra"}}}
		resolved, err := ResolveModel(model)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Effort).To(Equal(EffortMax))
		Expect(resolved.Fallbacks[0].Effort).To(Equal(EffortMax))
		Expect(model.Name).To(Equal("agent:sonnet:ultra"))
		Expect(model.Fallbacks[0].Name).To(Equal("api:sonnet:ultra"))
	})
})
